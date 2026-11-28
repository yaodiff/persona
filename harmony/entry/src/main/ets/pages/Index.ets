import { PhoneBackgroundActivity } from '../components/PhoneBackgroundActivity';
import { PhoneChatForward } from '../models/PhoneChatForward';
import { PhoneChatForwardPicker, PhoneForwardNewTray } from '../components/PhoneChatForward';
import { PhoneMessageLiftHost, PhoneMessageLiftHostLayer } from '../components/PhoneMessageLift';
import { AssistantChatStateMark, AssistantChatTitle } from '../components/AssistantChatMarks';
import { assistantChatSource, assistantChatState, assistantSidebarChats, sidebarConversationLabel, sidebarConversationTitle, SidebarTaskHistory } from '../models/AssistantRecentChats';
import { i18n, intl } from '@kit.LocalizationKit';
import { nativeMeetingServices, NativeMeetingServices } from '../platform/NativeMeetingServices';
import { AgentStartPicker } from '../components/AgentStartPicker';
import { AgentLaunchChoices, builtinLauncherTitle, providerStartModes, LaunchChoice, RecentAgentSetup, loadAgentSetups, rememberAgentSetup, recentLaunchDirectories } from '../models/AgentLaunchChoices';
import { WorktreeSheet } from '../components/WorktreeSheet';
import { phonePlaceAtRoot, phonePanIntent, finishesPhonePan } from '../models/PhoneNavigation';
import { relativeActivityDate } from '../core/PhoneDate';
import { PhoneConversationSearch } from '../components/PhoneConversationSearch';
import { PhoneAllChats } from '../components/PhoneAllChats';
import { SessionSpaceTabs } from '../components/SessionSpaceTabs';
import { PhoneSessionHeading } from '../components/PhoneSessionHeading';
import { AgentReadingBar } from '../components/AgentReadingBar';
import { AgentReadingHeader } from '../models/AgentReadingHeader';
import { SessionSpace, SessionSpaceTab, sessionSpace, worktreeForSessionSpace } from '../models/SessionSpace';
import { PhoneFirstRun } from '../components/PhoneFirstRun';
import { PhonePairingForm } from '../components/PhonePairingForm';
import { PairingSshForm } from '../components/PairingSshForm';
import { PairingSsh } from '../models/PairingSsh';
import { phonePasswordLogin } from '../platform/PhonePasswordLogin';
import { photoAccessHelper } from '@kit.MediaLibraryKit';
import { MachineDirectory } from '../components/MachineDirectory';
import { PhoneIconButton, PhoneNavigationIcon, PhoneStyle } from '../design/PhoneStyle';
import { PhoneOutlineIcon } from '../design/PhoneOutlineIcon';
import { phoneAppearancePage, phoneSettingsLink, pushPhoneSetting } from '../core/PhoneSettingsNavigation';
import { SettingsDirectory, PhoneSettingsOptions } from '../components/SettingsDirectory';
import { PhoneAlarmsSettings } from '../components/PhoneAlarmsSettings';
import { XiaoYiSettingsPage } from '../components/XiaoYiSettingsPage';
import { PhoneSettingsDevicePages } from '../components/PhoneSettingsDevicePages';
import { PhoneDefaultPermissionSection } from '../components/PhoneDefaultPermissionSection';
import { InboxSourcesNavigation } from '../models/InboxSources';
import { AllChatActions, ChatDirection } from '../models/AllChatActions';
import { PhoneInboxSources } from '../components/PhoneInboxSources';
import { WorktreeChange, WorktreeActions } from '../models/WorktreeActions';
import { ProjectCatalog, ProjectWorktree, PersonaProject } from '../models/ProjectCatalog';
import { ProjectsPage } from '../components/ProjectsPage';
import { IconActions } from '../design/IconActions';
import { ThemeScope } from '../design/ThemeScope';
import { util } from '@kit.ArkTS';
import { PhoneAgentSettingsPage } from '../components/PhoneAgentSettingsPage';
import { PhoneMeetingSettings } from '../components/PhoneMeetingSettings';
import { MeetingInputChoice, PhoneMeetingOptions } from '../models/PhoneMeetingOptions';
import { KnowledgeLandingMenu } from '../models/KnowledgeListPresentation';
import { PhoneKnowledgeMap } from '../components/PhoneKnowledgeMap';
import { PhoneKnowledgeSelection } from '../components/PhoneKnowledgeSelection';
import { AdminWorkspacePage } from '../components/AdminWorkspacePage';
import { ExtensionPage } from '../components/ExtensionPage';
import { ExtensionNavigationPage } from '../components/ExtensionNavigationPage';
import { ExtensionNavigation, ExtensionNavigationEntry } from '../models/ExtensionNavigation';
import { extensionIcon } from '../design/ExtensionIcons';
import type { ExtensionUINavigationEffect } from '../models/ExtensionUI';
import { ComputerScreenPage } from '../components/ComputerScreenPage';
import { isPairingAddress } from '../core/WireCompatibility';
import { detectBarcode, scanBarcode, scanCore } from '@kit.ScanKit';
import { BusinessError } from '@kit.BasicServicesKit';
import { RemoteDocumentPage } from '../components/RemoteDocumentPage';
import { TodoWorkspacePage } from '../components/TodoWorkspacePage';
import { KnowledgeWorkspacePage } from '../components/KnowledgeWorkspacePage';
import { HealthWorkspacePage } from '../components/HealthWorkspacePage';
import { NavigationContext } from '../models/NavigationContext';
import { StarredMessagesSheet } from '../components/StarredMessagesSheet';
import { ChatExportSheet } from '../components/ChatExportSheet';
import { ArchivedConversationsSheet } from '../components/ArchivedConversationsSheet';
import type { StarredSnapshot } from '../core/StarredMessages';
import { starredStore } from '../models/StarredStore';
import { LinkReader } from '../components/LinkReader';
import { readerLink, readerHost } from '../platform/ReaderLink';
import { common } from '@kit.AbilityKit';
import { DevicePage } from '../components/DevicePage';
import { deviceFeatures } from '../models/DeviceFeatures';
import { SessionTools } from '../components/SessionTools';
import { SessionWorkspace } from '../models/SessionWorkspace';
import { sessionModeAvailable } from '../models/SessionModes';
import { hilog } from '@kit.PerformanceAnalysisKit';
import { ComposeListItemV2, ContentItemV2, KeyboardAvoidMode, TextModifier, SymbolGlyphModifier, CapsuleSegmentButtonV2, SegmentButtonV2Items, ColorMetrics, LengthMetrics, uiMaterial } from '@kit.ArkUI';
import { ConfigurationConstant } from '@kit.AbilityKit';
import { ClientModel, client } from '../models/ClientModel';
import { ResourceRow, HubProfile, Wire } from '../models/Wire';
import { BoundRow, reconcileBoundRows } from '../models/RowBinding';
import { Pairing, PairingTarget } from '../platform/Pairing';
import { Conversation } from '../components/Conversation';
import { AgentListPage } from '../components/AgentListPage';
import { InteractionHistoryPage } from '../components/InteractionHistoryPage';
import { InboxWorkspacePage } from '../components/InboxWorkspacePage';
import { inboxTier } from '../models/InboxWorkspace';
import { ResourcePage } from '../components/ResourcePage';
import { SettingsPage } from '../components/SettingsPage';
import { SshSettingsPage } from '../components/SshSettingsPage';
import { DeviceLook, deviceLook } from '../models/DeviceLook';
import { nativeLookTheme } from '../design/LookTheme';
import { deviceLookInterfaceTint, deviceLookSelectedFilterInk, deviceLookSurface, usesSystemStyleColors } from '../core/DeviceLook';
import { phoneInterfaceStyleID } from '../core/PhoneInterfaceStyle';
import { IconTypeV2 } from '@kit.ArkUI';
import { Tokens } from '../design/Tokens';
import { Colors } from '../design/Colors';
import { Symbols } from '../design/Symbols';
import { ButtonStyles } from '../design/Controls';
import { AgentWorkspace } from '../models/AgentWorkspace';
import { AgentTaskGrouping } from '../models/AgentTaskGrouping';
import { AgentDecisionQueue } from '../models/AgentDecisionQueue';
import { nativeEnvironment, NativeEnvironment } from '../platform/NativeEnvironment';
import { nativeSheetInset } from '../platform/NativeInsets';
import { mainConversationData } from '../core/DefaultConversation';
import { AssistantDetailPage } from '../components/AssistantDetailPage';
import { tr, trError } from '../design/Strings';
import { KeyCode } from '@kit.InputKit';
import { Motion } from '../design/Motion';
import { Haptics } from '../platform/Haptics';
import { HoverSplitGeometry, pickRelaySessionIndex, resolveFoldKeyboardShortcut, resolveFoldLayout, resolveFoldShortcut, resolveHoverSplitGeometry, widthClassFromBreakpoint } from '../core/FoldPosture';
import type { FoldAdaptiveLayout } from '../core/FoldPosture';
import { FoldInspectorPanel } from '../components/FoldInspectorPanel';
import { QuickCommandEntry } from '../core/QuickCommandPalette';
import { QuickCommandSheet } from '../components/QuickCommandSheet';
import { TokenUsageSheet } from '../components/TokenUsageSheet';
import { StandingAuthSheet } from '../components/StandingAuthSheet';
import { AssistantSearchSheet } from '../components/AssistantSearchSheet';
import { AutoConversationsSheet } from '../components/AutoConversationsSheet';
import { nativeLiveView } from '../platform/NativeLiveView';
import { FloatingToastOverlay } from '../components/FloatingToastOverlay';
import { toastCenter } from '../models/ToastCenter';
import { isHubStateKey } from '../core/StorageOwnership';
import { sessionDisplayTitle } from '../core/SessionNaming';
import { formatSharedDraftText } from '../core/XiaoYiRoutes';

interface ChatActionEntry extends SheetInfo {
  identity?: string;
}

const forwardRestingDetent = '56.875%';

@Entry
@ComponentV2
struct Index {
  @Local model: ClientModel = client;
  @Local look: DeviceLook = deviceLook;
  @Local detailModel: ClientModel = client;
  @Local chatReady: boolean = true;
  @Local detailRoute: string = '';
  @Local detailSelection: string = '';
  private detailOwner: ClientModel | undefined;
  @Local adminRefreshRevision: number = 0;
  @Local inboxRefreshRevision: number = 0;
  @Local inboxComposeRevision:number=0;
  @Local inboxComposeAvailable:boolean=false;
  private inboxComposeScope:string='';
  @Local meetingOptions:PhoneMeetingOptions=new PhoneMeetingOptions();
  @Local knowledgeMenu:KnowledgeLandingMenu=new KnowledgeLandingMenu();
  @Local moduleMenuShown:boolean=false;
  @Local moduleMenuOwner:string='';
  @Local moduleMenuKind:string='';
  @Local recordingEditRevision: number = 0;
  @Local extensionTitle: string = '';
  @Local todoTitle: string = '';
  @Local todoCreateRevision: number = 0;
  @Local extensionRefreshRevision: number = 0;
  @Local messageLift: PhoneMessageLiftHost = new PhoneMessageLiftHost();
  @Local chatForward: PhoneChatForward = new PhoneChatForward();
  @Local applications: ExtensionNavigation = new ExtensionNavigation();
  @Local shownName: string = '';
  @Local phonePath: string[] = [];
  @Local phoneParam: string = '';
  @Local shownParam: string = '';
  @Local chatMenuActions: ChatActionEntry[] = [];
  @Local chatTitleMenuVisible: boolean = false;
  @Local activityRow: ResourceRow | undefined = undefined;
  @Local activityVisible: boolean = false;
  @Local chatBodyMenuVisible: boolean = false;
  @Local chatMenuRevision: number = 0;
  private routeRevision: number = 0;
  @Local stack: NavPathStack = new NavPathStack();
  @Local breakpoint: WidthBreakpoint = WidthBreakpoint.WIDTH_SM;
  @Local collapsed: boolean = false;
  @Local foldInspectorOpen: boolean = false;
  @Local foldInspectorUserToggled: boolean = false;
  @Local foldWorkbenchTab: number = 0;
  @Local foldWorkbenchTabSignal: number = 0;
  @Local quickPaletteOpen: boolean = false;
  @Local tokenUsageOpen: boolean = false;
  @Local private workbenchInterruptKey: string = '';
  private foldInspectorCollapsedRail: boolean = false;
  private lastFoldPosture: string = '';
  @Local foldWindowWidthVp: number = 0;
  @Local foldWindowHeightVp: number = 0;
  @Local drawerSearch: string = '';
  @Local drawerSearching: boolean = false;
  @Local projectSearching: boolean = false;
  @Local agentSearching: boolean = false;
  @Local peerDrag: number = 0;
  @Local recentExpanded: boolean = true;
  @Local chatSearchShown: boolean = false;
  @Local startSearchInAgents: boolean = false;
  @Local drawerAgentsExpanded: boolean = false;
  @Local pendingSearchHit: string = '';
  private pendingSearchTarget: string = '';
  @Local drawerFraction: number = 0;
  @Local drawerMounted: boolean = false;
  @Local phoneWidth: number = 0;
  private phoneHeight: number = 0;
  private gestureKind: string = '';
  private gestureScope: string = '';
  private gesturePlace: number = -1;
  private drawerFrom: number = 0;
  private drawerAnimation: number = 0;
  private drawerVelocity: number = 0;
  private drawerTapBlockedUntil: number = 0;
  @Local query: string = '';
  @Local filter: number = 0;
  @Local sidebarTaskHistory: SidebarTaskHistory = new SidebarTaskHistory();
  @Local directionRowBindings: BoundRow<ResourceRow>[] = [];
  @Local conversationRowBindings: BoundRow<ResourceRow>[] = [];
  private conversationBindingOwner: Object | undefined;
  private conversationBindingScope: string = '';
  @Local visibilityEpoch: number = 0;
  private rowVisibility: Map<string, number> = new Map();
  @Local readerAddress: string = '';
  @Local readerTitle: string = '';
  @Local readerShown: boolean = false;
  @Local sheetKind: string = '';
  @Local showSheet: boolean = false;
  @Local address: string = '';
  @Local pairCode: string = '';
  @Local pairManually: boolean = false;
  @Local pairByPassword: boolean = false;
  @Local passwordAuthorizing: boolean = false;
  @Local pairAccessExpanded: boolean = false;
  @Local pairAccessID: string = '';
  @Local pairAccessSecret: string = '';
  @Local pairSsh: PairingSsh = new PairingSsh();
  @Local editingPairSsh: boolean = false;
  private pairingRevision: number = 0;
  @Local scanning: boolean = false;
  private scanRevision: number = 0;
  @Local confirmingScan: boolean = false;
  @Local resourceKind: string = 'todo';
  @Local newTask: string = '';
  @Local agentTabName: string = '';
  @Local creatingAgent: boolean = false;
  @Local agentModel: ClientModel = client;
  @Local agentTodo: ResourceRow | undefined = undefined;
  private agentFormRevision: number = 0;
  @Local launcher: string = '';
  @Local directory: string = '';
  @Local settingsPage: string = '';
  @Local inboxSourcesNav:InboxSourcesNavigation=new InboxSourcesNavigation();
  @Local directionPermissionRoutes:AllChatActions=new AllChatActions();
  @Local directionPermissionsVisible:boolean=false;
  @Local private starredSheetShown: boolean = false;
  @Local private exportSheetShown: boolean = false;
  @Local private archivedSheetShown: boolean = false;
  @Local private standingAuthSheetShown: boolean = false;
  @Local private fullSearchSheetShown: boolean = false;
  @Local private autoChatsSheetShown: boolean = false;
  @Local private forkSessionTarget: string = '';
  private directionPermissionsTarget:string='';
  private pairingSettingsOwner:string='';
  @Local activeChild: string = '';
  @Local tool: number = 0;
  private titleMaterial: uiMaterial.Material = uiMaterial.isImmersiveMaterialSupported() ? new uiMaterial.ImmersiveMaterial({ style: uiMaterial.ImmersiveStyle.THIN }) : uiMaterial.Material.empty;
  @Local projectRootRevision: number = 0;
  @Local projectCatalog: ProjectCatalog = new ProjectCatalog();
  @Local agentWorktree: ProjectWorktree | undefined = undefined;
  @Local pickedAgentTree: string = '';
  @Local agentLauncherOptions: string[] = [];
  @Local agentLauncherLabels: string[] = [];
  @Local launchChoices: AgentLaunchChoices = new AgentLaunchChoices();
  @Local agentPanel: string = '';
  @Local agentMachineKey: string = '';
  @Local switchingAgentMachine: boolean = false;
  @Local agentModelId: string = '';
  @Local agentEffort: string = '';
  @Local defaultLauncherName: string = '';
  @Local agentUsesModelSettings: boolean = false;
  @Local agentConfiguredModel: string = '';
  @Local launcherData: Record<string,Object>[] = [];
  @Local blockedLaunchers: LaunchChoice[] = [];
  @Local launchListReady: boolean = false;
  @Local recentSetups: RecentAgentSetup[] = [];
  @Local recentAgentDirectories: LaunchChoice[] = [];
  private agentFormScroll: Scroller = new Scroller();
  private focusedAgentField: string = '';
  private agentFocusScrollOrigin: number = -1;
  private agentFocusLayoutRequest: number = 0;
  @Local agentErrorHeight: number = 0;
  @Local startFailure: string = '';
  @Local startWorktree: WorktreeActions = new WorktreeActions();
  private startTreeSource: ProjectWorktree | undefined;
  private machineIntent: number = 0;
  @Local agentPresets: Record<string,Object>[] = [];
  @Local agentTierProfile: string = '';
  @Local agentPreset: string = '';
  @Local machineDefaultLabel: string = '';
  private launcherRevision: number = 0;
  @Local agentWorkspace: AgentWorkspace = new AgentWorkspace(undefined,'repo');
  @Local environment: NativeEnvironment = nativeEnvironment;
  @Local spacesShown: boolean = false;
  @Local assistantMenuScope: string = '';
  private assistantMenuEpoch: number = 0;
  @Local assistantMenuNow: number = Date.now();
  private assistantStatusTimer: number = -1;
  @Local phoneSettingsShown: boolean = false;
  @Local phoneSettingsTrail: string[] = [];
  @Computed get phoneSettingsPage(): string { return this.phoneSettingsTrail[this.phoneSettingsTrail.length - 1] || ''; }
  @Local phoneSettingsQuery: string = '';
  private phoneSettingsDepth: number = -1;
  private phoneSettingsOwner: string = '';
  private phoneSettingsReturnRoute: string = 'settings';
  @Local phoneSettingsMachine: ResourceRow | undefined = undefined;
  @Local spacesInset: number = 0;
  private spacesLayoutReady: boolean = false;
  @Local inlineStopIdentity: string = '';
  private defaultMainAttempt: string = '';
  private navigationRestoredScope: string = '';
  private navigationIntent: number = 0;
  private mainReadWaiters: (() => void)[] = [];
  private defaultMainPending: boolean = false;
  private creatingConversations: Map<string, number> = new Map();
  private paths: Map<string, NavPathStack> = new Map();
  private previousPrimary: number = 0;
  private activeScope: string = '';
  private restoring: number = 0;
  private peer(): number { return this.model.section === 1 ? 1 : 0; }
  private headerHeight(): number { return Tokens.header; }
  @Local pendingInboxHandoff: string = '';
  @Local mainRowVisible: number = 0;
  private mainDestination(name: string, param: Object): boolean {
    if (name !== 'conversation') return false;
    const row = this.routeRow(param);
    return name === 'conversation' && Wire.bool(row.data(), 'navigation_root') && row.id === this.model.mainId && (!row.group || row.group === this.model.profile.id) &&
      (!row.manager || row.manager === this.model.profile.manager) && (!this.model.mainMachineId || row.machine === this.model.mainMachineId);
  }
  private hideChatTitle(name: string, param: Object): boolean {
    if (name !== 'session' && name !== 'conversation') return false;
    if (!Wire.bool(this.routeRow(param).data(), 'navigation_root')) return false;
    if (name === 'session') return this.hideAgentTitle();
    const row = this.routeRow(param), epoch = this.visibilityEpoch;
    const visible = row.id === this.model.mainId && row.machine === this.model.mainMachineId ? this.mainRowVisible : this.rowVisibility.get(this.model.scope() + this.model.target(row)) || 0;
    return name === 'conversation' && this.detailModel === this.model && this.peer() === 0 && this.wide() && !this.rail() &&
      this.model.target() === this.model.target(row) && visible >= 0.99;
  }
  @Builder peerTitle(_main: boolean = false) {
    Row({ space: Tokens.gap }) {
      CapsuleSegmentButtonV2({ items: new SegmentButtonV2Items([
        { symbol: Symbols.persona, accessibilityText: tr('persona') },
        { symbol: Symbols.agents, accessibilityText: tr('agents') }]),
        selectedIndex: this.peer(), $selectedIndex: (index: number) => this.selectPeer(index),
        itemSelectedBackgroundColor: ColorMetrics.resourceColor(Colors.action),
        itemSymbolFontColor: ColorMetrics.resourceColor(Colors.secondary),
        itemSelectedSymbolFontColor: ColorMetrics.resourceColor(Colors.onAction),
        itemSymbolFontSize: LengthMetrics.vp(Tokens.symbol),
        buttonBackgroundColor: ColorMetrics.resourceColor(Colors.control),
        itemMinHeight: LengthMetrics.vp(Tokens.touch), buttonMinHeight: LengthMetrics.vp(Tokens.touch) })
        .width(Tokens.headerPeerIcons).id('shell.peer')
      Blank().layoutWeight(1)
      this.shellMenu()
    }.width('100%').height(this.headerHeight()).padding({ left: Tokens.page }).alignItems(VerticalAlign.Center)
  }
  private projectPlace(): boolean {
    return this.model.section === 4;
  }
  @Local meetingServices:NativeMeetingServices=nativeMeetingServices;
  @Monitor('meetingServices.pendingOpen','model.ready')
  meetingReminderRequested():void { this.consumeMeetingReminder(); }
  private consumeMeetingReminder():void {
    if (!this.meetingServices.pendingOpen || !this.model.ready || !this.model.profile.id || this.navigationRestoredScope!==this.model.scope()) return;
    this.meetingServices.pendingOpen=false; this.spacesShown=false;
    if (this.stack.getAllPathName().slice(-1)[0]!=='recordings') this.pushPlace('recordings','');
  }
  @Monitor('model.projectsRequested', 'model.ready')
  projectLaunchChanged(): void { this.consumeProjectLaunch(); }
  private consumeProjectLaunch(): void {
    if (!this.model.projectsRequested || !this.model.ready || !this.model.profile.id || this.navigationRestoredScope !== this.model.scope()) return;
    this.model.projectsRequested = false; this.projectRootRevision++;
    this.navigationIntent++; this.spacesShown = false;
    if (this.model.section !== 4) this.changeSection(4);
    this.stack.clear(); this.savePath(); this.projectCatalog.refresh(this.model);
  }
  private openProjects(): void {
    this.navigationIntent++; this.spacesShown = false;
    if (!this.projectPlace()) this.changeSection(4);
    else { this.stack.clear(); this.savePath(); }
    this.projectCatalog.refresh(this.model);
  }
  private pushPlace(name: string, key: string): void {
    this.stack.pushPath({ name: name, param: key }); this.savePath();
  }
  private browsePeer(index: number): void {
    if (index !== 0 && index !== 1) return;
    this.spacesShown = false;
    if (this.model.section !== index) this.changeSection(index);
    if (index === 1) this.ensureAgentWorkspace();
    else if (!this.stack.size()) this.openMain();
  }

  private mergedPhone():boolean { return !this.wide() && this.look.phoneNavigation === 'merged'; }
  @Local longDraftIdentity: string = '';
  @Local agentReadingHeader:AgentReadingHeader=new AgentReadingHeader();
  @Computed get readingHeaderOwner():string {
    if (this.wide() || this.phoneTop!=='session' || this.floatingDraftHeader || !this.phoneChatActionsReady ||
      this.mainDestination(this.phoneTop,this.phoneParam) || this.detailModel.selected.kind!=='session' ||
      Wire.bool(this.detailModel.selected.data(),'assistant')) return '';
    return JSON.stringify([this.activeChild,this.detailModel.scope(),this.detailModel.connectionGeneration(),this.detailModel.target()]);
  }
  private readingHeaderCovered():boolean {
    return this.showSheet || this.spacesShown || this.phoneSettingsShown || this.chatSearchShown || this.chatTitleMenuVisible ||
      this.chatBodyMenuVisible || !!this.messageLift.current || this.chatForward.isPicking(this.detailModel) ||
      this.chatForward.isChoosing(this.detailModel) || this.environment.keyboardHeightPx>0;
  }
  @Builder slimSessionHeader() {
    Row({ space: Tokens.spaceS }) {
      Button({ type: ButtonType.Circle }) {
        SymbolGlyph(Symbols.collapse).fontSize(Tokens.interfaceFontVp(16, this.environment.fontScale)).fontColor([Colors.ink])
      }.width(36).height(36).padding(0)
        .backgroundColor(Colors.styleID === 'glass' ? Colors.elevatedGlass : Colors.surface)
        .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
        .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: Colors.styleEdge || Colors.hairlineGlass, radius: 18 })
        .shadow(PhoneStyle.floatingElevation(this.environment.dark))
        .responseRegion(ButtonStyles.touchRegion(36))
        .accessibilityText(tr(this.phoneAtRoot ? 'conversation_spaces' : 'back'))
        .onClick(() => { if (this.phoneAtRoot) { this.drawerSearching = false; this.spacesShown = true; } else this.phoneBack(); })
        .bindContextMenu(this.backHistory(), ResponseType.LongPress).id(this.phoneAtRoot ? 'shell.spaces' : 'shell.back')
      PhoneSessionHeading({ model: this.detailModel, space: this.phoneSessionSpace, heading: this.phonePageName(), slim: true,
        expand: () => this.agentReadingHeader.reveal(this.readingHeaderOwner) }).layoutWeight(1)
      this.sessionMoreButton(true)
    }.width('100%').height(AgentReadingHeader.slimHeight).padding({ left: Tokens.spaceM, right: Tokens.spaceM }).alignItems(VerticalAlign.Center)
  }
  @Computed get floatingDraftHeader(): boolean {
    return this.fullHeightPhone() && this.phoneTop === 'session' && !!this.longDraftIdentity &&
      this.longDraftIdentity === this.activeChild;
  }
  @Computed get floatingProjectHeader(): boolean {
    return this.model.profile.id.length > 0 && !this.wide() && !this.mergedPhone() &&
      this.projectPlace() && !this.phoneTop && !this.projectSearching;
  }
  private syncPhonePath():void {
    this.phonePath = this.stack.getAllPathName().slice();
    const param: Object = this.phonePath.length ? this.stack.getParamByIndex(this.phonePath.length-1) as Object : '';
    this.phoneParam = typeof param === 'string' ? param : '';
  }
  @Computed get phoneTop():string { return this.phonePath[this.phonePath.length-1] || ''; }
  @Computed get phoneAtRoot():boolean {
    if (this.phoneTop === 'resources' && this.phoneParam === 'health' && !this.wide()) return false;
    const isRootChat = this.phonePath.length === 1 && (this.phoneTop === 'session' || this.phoneTop === 'conversation') &&
      Wire.bool(this.routeRow(this.phoneParam).data(),'navigation_root');
    if (this.mergedPhone() && this.model.section === 1 && this.phoneTop === 'session' && isRootChat) return true;
    return phonePlaceAtRoot(this.model.section,this.phonePath,this.phoneTop === 'conversation' && isRootChat);
  }
  private phonePeer(): boolean {
    return !this.mergedPhone() && this.phoneAtRoot && [0, 1, 4].includes(this.model.section);
  }
  private compactPeerIndex(): number { return this.model.section === 4 ? 1 : this.model.section; }
  private showAllChats(agents:boolean): void {
    this.startSearchInAgents = agents; this.drawerSearching = false;
    if (agents) this.spacesShown = false;
    this.chatSearchShown = true;
  }
  private hitIdentity(row:ResourceRow):string {
    return JSON.stringify([this.model.scope(),row.group || this.model.profile.id,row.manager || this.model.profile.manager,row.kind,row.machine,row.id]);
  }
  private searchHitFor(param:Object):string {
    return this.pendingSearchHit && this.pendingSearchTarget === this.hitIdentity(this.routeRow(param)) ? this.pendingSearchHit : '';
  }
  private decisionToolFor(param: Object): string | undefined {
    const data = this.routeRow(param).data();
    return typeof data['navigation_decision_tool'] === 'string' ? Wire.text(data,'navigation_decision_tool') : undefined;
  }
  private openPickedChat(row:ResourceRow,hit:string = ''): void {
    this.pendingSearchHit = hit; this.pendingSearchTarget = hit ? this.hitIdentity(row) : '';

    this.spacesShown = false; this.drawerSearching = false; this.chatSearchShown = false;
    this.fullSearchSheetShown = false; this.autoChatsSheetShown = false; this.standingAuthSheetShown = false; this.archivedSheetShown = false;
    const section = row.kind === 'session' ? 1 : 0;
    if (this.model.section !== section) this.changeSection(section);
    this.stack.clear();
    this.openRow(row, true);
  }
  private openActivity(): void {
    const row = this.model.mainActivityRow();
    if (!row) return;
    this.activityRow = row;
    this.activityVisible = true;
  }
  @Builder backgroundActivitySheet() {
    if (this.activityRow) {
      PhoneBackgroundActivity({ model: this.model, row: this.activityRow,
        onClose: () => { this.activityVisible = false; },
        onOpen: (row: ResourceRow) => {
          if (row.kind === 'session' && this.model.section !== 0) this.changeSection(0);
          this.openLinkedResource(row);
        },
        onLink: (address: string) => this.openReader(address),
        onCreateAgent: (row: ResourceRow) => { this.activityVisible = false; this.prepareAgent(row); } })
    }
  }
  @Builder allChatsSheet() {
    PhoneAllChats({model:this.model,history:this.sidebarTaskHistory,onClose:() => this.chatSearchShown = false,
      onOpen:(row:ResourceRow) => this.openPickedChat(row)})
  }
  @Builder phoneSearch() {
    PhoneConversationSearch({model:this.model,initialAgents:this.startSearchInAgents,
      onCancel:() => this.chatSearchShown = false, onOpen:(row:ResourceRow,hit:string) => this.openPickedChat(row,hit)})
  }
  @Builder selectedPeerMenu(index:number) {
    Menu() {
      if (index === 0) {
        MenuItem({content:tr('main_chat')}).onClick(() => { this.stack.clear(); this.openMain(); })
        MenuItem({content:tr('flow_all_chats')}).onClick(() => this.showAllChats(false)).id('shell.menu.chats')
        MenuItem({content:tr('new_chat')}).onClick(() => this.newConversation())
        if (this.model.mainActivityRow()) {
          MenuItem({content:tr('background_activity')}).onClick(() => this.openActivity()).id('background-activity-menu')
        }
      } else {
        if (this.model.section === 4) {
          MenuItem({content:tr('agents')}).onClick(() => this.browsePeer(1)).id('shell.menu.agents')
        } else {
          ForEach(['all','needsYou','unread','working'],(value:string) => {
            MenuItem({content:tr(value === 'needsYou' ? 'needs_you' : value === 'unread' ? 'agent_group_unread' : value)})
              .selected(this.agentWorkspace.quickFilter === value).selectIcon(true).onClick(() => this.agentWorkspace.setQuickFilter(value))
          },(value:string) => value)
          ForEach(['avatar','compact','cards','list'],(mode:string) => {
            MenuItem({content:tr('phone_agent_display_'+mode)})
              .selected(this.look.agentDisplay === mode).selectIcon(true)
              .onClick(() => { this.look.setAgentDisplay(mode); }).id('shell.agents.display.'+mode)
          },(mode:string) => mode)
        }
        MenuItem({content:tr('search_sessions')}).onClick(() => this.showAllChats(true))
      }
      MenuItem({content:tr('projects')}).selected(this.model.section === 4).selectIcon(true)
        .onClick(() => this.openProjects()).id('shell.menu.projects')
    }
  }
  @Builder placeButton(label:string,index:number,identity:string,action:() => void) {
    if (index === this.compactPeerIndex()) {
      Button(tr(label),{type:ButtonType.Capsule}).width(this.peerSegmentWidth()).height(this.peerSegmentHeight()).padding({left:3,right:3})
        .fontSize(Tokens.body).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Bold)
        .labelStyle({maxLines:1,overflow:TextOverflow.Ellipsis})
        .fontColor(Colors.ink).backgroundColor(Color.Transparent).responseRegion({x:0,y:-4,width:'100%',height:this.peerSegmentHeight()+8})
        .accessibilitySelected(true).accessibilityDescription(index===1 && this.waitingAgentCount ? tr('needs_you')+' '+this.waitingAgentCount : '')
        .bindMenu(this.selectedPeerMenu(index)).id(identity)
    } else {
      Button(tr(label),{type:ButtonType.Capsule}).width(this.peerSegmentWidth()).height(this.peerSegmentHeight()).padding({left:3,right:3})
        .fontSize(Tokens.body).maxFontScale(Tokens.interfaceFontMaxScale).fontWeight(FontWeight.Regular)
        .labelStyle({maxLines:1,overflow:TextOverflow.Ellipsis})
        .fontColor(Colors.secondary).backgroundColor(Color.Transparent).responseRegion({x:0,y:-4,width:'100%',height:this.peerSegmentHeight()+8})
        .accessibilitySelected(false).accessibilityDescription(index===1 && this.waitingAgentCount ? tr('needs_you')+' '+this.waitingAgentCount : '')
        .onClick(action).id(identity)
    }
  }
  private peerActionSize(): number { return PhoneStyle.formSize(48.3,57.7,this.environment.fontScale); }
  private sessionActionSize(): number { return PhoneStyle.formSize(44,57.7,this.environment.fontScale); }
  private peerSegmentHeight(): number { return PhoneStyle.formSize(39.7,47.3,this.environment.fontScale); }
  private peerSegmentWidth(): number {
    const paletteReserve = this.phonePeer() ? 46 : 0;
    const room = (this.phoneWidth - 2 * this.peerActionSize() - 64 - paletteReserve) / 2;
    return Math.max(48, Math.min(PhoneStyle.formSize(98,114,this.environment.fontScale), room));
  }
  private drawerWidth(): number { return Math.min(380, Math.max(1,this.phoneWidth * 0.82)); }
  @Monitor('spacesShown') drawerStateChanged(): void {
    if (!this.spacesShown && !this.wide()) this.assistantMenuScope = '';
    if (this.wide()) { this.drawerFraction = 0; this.drawerMounted = false; this.drawerVelocity = 0; return; }
    const velocity = this.drawerVelocity;
    this.drawerVelocity = 0;
    this.settlePhoneDrawer(this.spacesShown, velocity);
  }
  private settlePhoneDrawer(open:boolean, velocity:number = 0): void {
    const revision = ++this.drawerAnimation;
    if (this.environment.reduceMotion) {
      this.drawerFraction = open ? 1 : 0;
      this.drawerMounted = open;
      return;
    }
    if (open) this.drawerMounted = true;
    const param = Motion.drawer(this.environment.reduceMotion, velocity, this.drawerWidth(), () => {
      if (revision === this.drawerAnimation && !this.spacesShown) this.drawerMounted = false;
    });
    this.getUIContext().animateTo(param, () => this.drawerFraction = open ? 1 : 0);
  }
  private startPhonePan(event:GestureEvent): void {
    this.gestureKind = '';
    if (this.wide() || !this.model.profile.id || this.showSheet || this.phoneSettingsShown || this.readerShown || this.chatSearchShown || this.quickPaletteOpen) return;
    const finger = event.fingerList[0];
    if (!finger) return;
    const x = finger.localX - event.offsetX, y = finger.localY - event.offsetY;
    this.gestureKind = phonePanIntent(this.mergedPhone() ? -1 : this.compactPeerIndex(),this.phoneAtRoot,this.spacesShown,x,y,this.phoneWidth,this.phoneHeight,event.offsetX,event.offsetY);
    this.gestureScope = this.model.scope(); this.gesturePlace = this.model.section;
    this.drawerFrom = this.drawerFraction;
    if (this.mergedPhone() && (this.gestureKind === 'persona' || this.gestureKind === 'agents')) this.gestureKind = '';
    if (this.gestureKind === 'open' || this.gestureKind === 'close') {
      this.drawerAnimation++; this.drawerMounted = true;
    }
  }
  private drawerAcceptsTap():boolean {
    return this.wide() || this.spacesShown && this.gestureKind !== 'close' && Date.now() >= this.drawerTapBlockedUntil;
  }
  private movePhonePan(dx:number): void {
    if (this.environment.reduceMotion) return;
    if (this.gestureKind === 'open' || this.gestureKind === 'close')
      this.drawerFraction = Math.max(0,Math.min(1,this.drawerFrom + dx / this.drawerWidth()));
    else if (this.gestureKind === 'agents' || this.gestureKind === 'persona') {
      const here = this.compactPeerIndex();
      this.peerDrag = Math.max(-here, Math.min(1-here, -dx/Math.max(this.phoneWidth,1)));
    }
  }
  private endPhonePan(dx:number,vx:number): void {
    const kind = this.gestureKind; this.gestureKind = '';
    if (!kind) return;
    if (kind === 'agents' || kind === 'persona') this.peerDrag = 0;
    if (this.gestureScope !== this.model.scope() || this.model.section !== this.gesturePlace) {
      this.settlePhoneDrawer(this.spacesShown); return;
    }
    const done = finishesPhonePan(dx,vx,kind === 'open' || kind === 'close' ? this.drawerWidth() : this.phoneWidth);
    if (kind === 'close') this.drawerTapBlockedUntil = Date.now() + 200;
    if (kind === 'open' || kind === 'close') {
      const open = done ? kind === 'open' && dx > 0 || kind === 'close' && dx >= 0 : this.spacesShown;
      if (open !== this.spacesShown) { Haptics.selection(); this.drawerSearching = false; this.drawerVelocity = vx; this.spacesShown = open; }
      else this.settlePhoneDrawer(open, vx);
    } else if (done && this.phonePeer() && (kind === 'agents' && dx < 0 || kind === 'persona' && dx > 0)) {
      Haptics.selection();
      this.browsePeer(kind === 'agents' ? 1 : 0);
    }
  }
  private judgePhonePan(event: BaseGestureEvent, current: GestureRecognizer, peers: GestureRecognizer[]): GestureJudgeResult {
    if (current.getTag() !== 'phone.shell.pan') return GestureJudgeResult.CONTINUE;
    const dx = (event as PanGestureEvent).offsetX;
    for (const peer of peers) {
      if (!peer.isBuiltIn() || !(peer instanceof PanRecognizer) || peer.getDirection() !== PanDirection.Horizontal) continue;
      const target = peer.getEventTargetInfo();
      if (target instanceof ScrollableTargetInfo && (dx < 0 && !target.isEnd() || dx > 0 && !target.isBegin())) return GestureJudgeResult.REJECT;
    }
    return GestureJudgeResult.CONTINUE;
  }
  @Computed get phoneToolParent(): string {
    const parent = this.phonePath.length - 2;
    if (this.phoneTop !== 'tools' || parent < 0 || this.phonePath[parent] !== 'session') return '';
    const value: Object = this.stack.getParamByIndex(parent) as Object;
    if (typeof value !== 'string') return '';
    return this.routeRow(value).key() === this.routeRow(this.phoneParam).key() ? value : '';
  }
  private phonePageName():string {
    if (this.phoneToolParent) return this.chatTitle(this.phoneToolParent);
    if (this.phoneTop) return this.destinationTitleText(this.phoneTop,this.phoneParam);
    return tr(this.model.section === 4 ? 'projects' : this.model.section === 1 ? 'agents' : 'persona');
  }
  @Computed get phoneSessionSpace():SessionSpace {
    const name = this.phoneTop;
    if ((name !== 'session' && !this.phoneToolParent) || this.activeChild !== this.childKey(name,this.phoneParam) ||
      !this.targetReady(name,this.phoneParam) || !this.detailModel.connected) return new SessionSpace();
    const owner = this.detailModel;
    return sessionSpace(owner.selected,owner.sessions,this.projectCatalog.projects,owner.profile.id,owner.profile.manager);
  }
  private switchSessionTab(key:string,from:string,id:string):void {
    const space = this.phoneSessionSpace;
    if (!key || key !== space.key || from !== space.current || id === from || !this.stack.size()) return;
    const tab = space.tabs.find((item:SessionSpaceTab) => item.id === id);
    if (!tab) return;
    const top = this.stack.size()-1;
    const parent = this.phoneToolParent, name = parent ? 'tools' : 'session';
    if (this.stack.getAllPathName()[top] !== name || this.stack.getParamByIndex(top) !== this.phoneParam) return;
    const old = this.routeRow(parent || this.phoneParam), target = this.routeTarget(tab.row,Wire.bool(old.data(),'navigation_root'));
    target.group = this.detailModel.profile.id; target.manager = this.detailModel.profile.manager;
    if (this.model.section === 4) this.projectCatalog.browser.selectedSession = target.key();
    this.pendingSearchTarget = ''; this.pendingSearchHit = ''; this.chatReady = false; this.detailRoute = ''; this.tool = 0;
    if (parent) this.stack.pop(false);
    this.stack.replacePath({name:'session',param:JSON.stringify(target)},false);
    this.savePath();
  }
  private async openSessionSpace(key: string): Promise<void> {
    if (!key || key !== this.phoneSessionSpace.key) return;
    const origin = this.childKey(this.phoneTop, this.phoneParam);
    const selected = this.phoneSessionSpace.current;
    const owner = this.model, scope = owner.scope(), generation = owner.connectionGeneration();
    if (!worktreeForSessionSpace(this.phoneSessionSpace, this.projectCatalog.projects)) await this.projectCatalog.refresh(owner);
    const live = this.phoneSessionSpace;
    if (!owner.owns(generation, scope) || this.activeChild !== origin || key !== live.key || live.current !== selected) return;
    const tree = worktreeForSessionSpace(live, this.projectCatalog.projects);
    if (tree) this.pushPlace('project_tree', tree.key);
  }
  private phoneLargeTitle():boolean {
    if (!this.phoneTop) return this.mergedPhone() && (this.model.section === 1 || this.model.section === 4);
    return ['project','applications'].includes(this.phoneTop) ||
      this.phoneTop === 'resources' && this.phoneParam === 'health' ||
      this.phoneTop === 'settings' && this.phonePath.length === 1 && ['machines','schedules'].includes(this.settingsKind(this.phoneParam));
  }
  private returnToPhoneSettings(): boolean {
    if (this.phoneSettingsDepth < 0 || this.stack.size() !== this.phoneSettingsDepth + 1 || this.stack.getAllPathName()[this.stack.size() - 1] !== this.phoneSettingsReturnRoute ||
      this.phoneSettingsOwner !== this.model.scope() || this.wide()) return false;
    this.phoneSettingsDepth = -1; this.stack.pop(); this.savePath();
    this.spacesShown = true; this.phoneSettingsShown = true; return true;
  }
  private phoneBack(): void {
    if (!this.stack.size() || this.returnToPhoneSettings()) return;
    this.detailModel.releaseTerminal();
    if (this.model.section >= 2 && this.model.section !== 4 && this.stack.size() === 1) this.changeSection(this.previousPrimary);
    else { this.stack.pop(); this.savePath(); }
  }
  private startFromPlace(): void {
    if (this.projectPlace()) {
      const list = this.stack.getAllPathName(), pos = list.lastIndexOf('project_tree');
      const tree = pos >= 0 ? this.projectCatalog.tree(this.stack.getParamByIndex(pos) as string) : undefined;
      this.prepareAgent(undefined, tree?.canStart ? tree : undefined);
    } else if (this.peer() === 1) this.openSheet('newagent');
    else this.newConversation();
  }
  @Builder phoneHeader() {
    if (this.readingHeaderOwner) AgentReadingBar({state:this.agentReadingHeader,owner:this.readingHeaderOwner,
      covered:():boolean=>this.readingHeaderCovered(),full:():void=>this.themedPhonePlaces(),slim:():void=>this.slimSessionHeader()})
    else this.themedPhonePlaces()
  }
  @Builder themedPhonePlaces() {
    if (this.phoneTop === 'session' || this.phoneToolParent) {
      // A tool push retains the session header outside Navigation. Renew only its
      // native resource scope when appearance changes, leaving both destinations mounted.
      ForEach([this.environment.dark], (dark:boolean) => {
        WithTheme({ theme:nativeLookTheme(this.look,this.model.style,dark), colorMode:dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT }) {
          this.compactPlaces()
        }
      }, (dark:boolean) => String(dark))
    } else this.compactPlaces()
  }
  private rootModuleMenuKind():string {
    if (this.phoneTop==='recordings' && this.meetingOptions.available) return 'recordings';
    if (this.phoneTop==='knowledge_meetings') return 'knowledge';
    if (this.phoneTop==='resources' && ['knowledge','inbox'].includes(this.phoneParam)) return this.phoneParam;
    return this.model.section===2 && !this.phoneTop ? 'inbox' : '';
  }
  private menuOwner(kind:string):string {
    const query=kind==='knowledge' ? this.knowledgeMenu?.query || '' : '';
    return JSON.stringify([kind,this.model.scope(),this.model.connectionGeneration(),this.model.section,this.phoneTop,this.phoneParam,this.navigationIntent,query]);
  }
  private inboxPermissionOwner():string { return JSON.stringify([this.model.scope(),this.model.connectionGeneration()]); }
  private canComposeFromMenu():boolean { return this.model.connected && this.inboxComposeAvailable && this.inboxComposeScope===this.inboxPermissionOwner(); }
  private menuCurrent(kind:string):boolean {
    return this.rootModuleMenuKind()===kind && !this.spacesShown && this.moduleMenuKind===kind && this.moduleMenuOwner===this.menuOwner(kind);
  }
  @Builder rootModuleMenu() {
    Menu() {
      if (this.moduleMenuKind==='knowledge') {
        MenuItem({content:tr('all')}).selectIcon(true).selected(!this.knowledgeMenu.selected)
          .accessibilitySelected(!this.knowledgeMenu.selected)
          .onClick(()=>{if(this.menuCurrent('knowledge')) this.knowledgeMenu.choose('');}).id('knowledge.filter.all')
        if (this.knowledgeMenu.tags.length) MenuItemGroup({header:tr('knowledge_frequent_tags')}) {
          ForEach(this.knowledgeMenu.tags,(tag:string)=>{
            MenuItem({content:'#'+tag}).selectIcon(true).selected(this.knowledgeMenu.selected===tag)
              .accessibilitySelected(this.knowledgeMenu.selected===tag)
              .onClick(()=>{if(this.menuCurrent('knowledge')) this.knowledgeMenu.choose(tag);}).id('knowledge.filter.'+tag)
          },(tag:string)=>tag)
        }
        MenuItem({content:tr('ks_map_title')}).onClick(()=>{if(this.menuCurrent('knowledge'))this.knowledgeMenu.map();}).id('knowledge.map')
      } else if (this.moduleMenuKind==='recordings') {
        if (this.meetingOptions.allowsPrivate) MenuItem({content:tr('phone_meeting_private')}).contentFont({size:Tokens.interfaceFontVp(16,this.environment.fontScale)}).selected(this.meetingOptions.marked).selectIcon(true).accessibilitySelected(this.meetingOptions.marked)
          .onClick(()=>{if(this.menuCurrent('recordings'))this.meetingOptions.togglePrivate();}).id('recordings.options.private')
        MenuItemGroup({header:tr('language')}) {
          if (this.meetingOptions.reading) MenuItem({content:tr('loading')}).enabled(false)
          else if (this.meetingOptions.languageAvailable) MenuItem({content:tr('meeting_language_chinese')}).contentFont({size:Tokens.interfaceFontVp(16,this.environment.fontScale)}).selected(true).selectIcon(true).enabled(false)
            .id('recordings.options.language.zh-CN')
          else MenuItem({content:tr('meeting_language_unavailable')}).onClick(()=>{if(this.menuCurrent('recordings'))this.meetingOptions.prepare();})
            .id('recordings.options.language.retry')
        }
        if (this.meetingOptions.inputs.length) MenuItemGroup({header:tr('meeting_input_label')}) {
          MenuItem({content:tr('meeting_input_automatic')}).contentFont({size:Tokens.interfaceFontVp(16,this.environment.fontScale)}).selected(this.meetingOptions.selectedInput<0).selectIcon(true)
            .onClick(()=>{if(this.menuCurrent('recordings'))this.meetingOptions.selectInput(-1);}).id('recordings.options.input.auto')
          ForEach(this.meetingOptions.inputs,(input:MeetingInputChoice)=>{
            MenuItem({content:input.bluetooth ? input.name || tr('meeting_bluetooth_input') : tr('meeting_phone_input')})
              .selected(this.meetingOptions.selectedInput===input.id).selectIcon(true)
              .onClick(()=>{if(this.menuCurrent('recordings'))this.meetingOptions.selectInput(input.id);}).id('recordings.options.input.'+input.id)
          },(input:MeetingInputChoice)=>String(input.id))
        }
      } else if (this.moduleMenuKind==='inbox') {
        if (this.model.mainActivityRow()) {
          MenuItem({content:tr('background_activity')}).onClick(() => { if (this.menuCurrent('inbox')) this.openActivity(); })
            .id('inbox-background-activity')
        }
        if (this.model.features.includes('inbox_sources')) MenuItem({content:tr('phone_inbox_sources')})
          .onClick(()=>{if(this.menuCurrent('inbox') && this.model.connected && this.model.features.includes('inbox_sources'))this.openSettings('phone_inbox_sources');})
          .id('inbox.more.sources')
        if (this.model.mailModuleAllowed()) MenuItem({content:tr('mail_compose')}).enabled(this.canComposeFromMenu())
          .onClick(()=>{if(this.menuCurrent('inbox') && this.canComposeFromMenu())this.inboxComposeRevision++;}).id('inbox.more.compose')
        MenuItem({content:tr('refresh')}).enabled(this.model.connected)
          .onClick(()=>{if(this.menuCurrent('inbox') && this.model.connected)this.inboxRefreshRevision++;}).id('inbox.more.refresh')
      }
    }
  }
  private openModuleMenu(kind:string,owner:string):void {
    if (this.spacesShown || this.rootModuleMenuKind()!==kind || this.menuOwner(kind)!==owner) return;
    this.moduleMenuKind=kind;this.moduleMenuOwner=owner;
    if (kind==='recordings') this.meetingOptions.prepare();
    this.moduleMenuShown=true;
  }
  private closeModuleMenu(owner:string):void {
    if (this.moduleMenuOwner===owner) this.moduleMenuShown=false;
  }
  @Builder phoneModuleMore(kind:string,owner:string) {
    Button({type:ButtonType.Circle}) {
      Row({space:3}) {
        ForEach([0,1,2], (_dot:number) => {
          Circle().width(3.5).height(3.5).fill(Colors.ink)
        }, (dot:number) => String(dot))
      }
    }.width(PhoneStyle.bar).height(PhoneStyle.bar).padding(0)
      .backgroundColor(Colors.styleID === 'glass' ? Colors.elevatedGlass : Colors.surface)
      .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:PhoneStyle.floatingBorder(this.environment.dark),radius:PhoneStyle.bar/2})
      .shadow(PhoneStyle.floatingElevation(this.environment.dark))
      .accessibilityText(tr(kind==='recordings' ? 'meeting_start_options' : 'more'))
      .id(kind==='recordings' ? 'recordings.options' : kind==='knowledge' ? 'knowledge.filters' : 'inbox.more')
      .onClick(()=>this.openModuleMenu(kind,owner))
      .bindMenu(this.moduleMenuShown && this.moduleMenuKind===kind && this.moduleMenuOwner===owner,this.rootModuleMenu,
        {placement:Placement.BottomRight,onDisappear:()=>this.closeModuleMenu(owner)})
  }
  private compactPaletteOnRight(): boolean {
    return this.phonePeer() || this.projectPlace() && (!this.phoneTop || this.phoneTop === 'project' || this.phoneTop === 'project_tree') || this.mergedPhone() && this.phoneAtRoot && (this.model.section === 0 || this.model.section === 1 && !this.phoneTop);
  }
  @Builder compactPlaces() {
    Column() {
      Row({space: 6}) {
        ForEach([this.phoneAtRoot ? 'root' : 'back'],(role:string) => {
          if (role === 'root') {
            if (this.phonePeer()) {
              Button({type:ButtonType.Circle}) {
                Stack({alignContent: Alignment.BottomEnd}) {
                  Image(this.look.avatarUri || $r('app.media.phone_persona_face')).width(this.peerActionSize()).height(this.peerActionSize())
                    .objectFit(ImageFit.Cover).borderRadius(this.peerActionSize()/2)
                  if (this.shellHasInboxAction) Circle().width(12).height(12).fill(Colors.needs)
                    .stroke(Colors.canvas).strokeWidth(2).offset({x:-4,y:-4}).accessibilityLevel('no')
                }
              }.width(this.peerActionSize()).height(this.peerActionSize()).padding(0).backgroundColor(Color.Transparent)
                .accessibilityText(tr('conversation_spaces'))
                .onClick(() => { this.drawerSearching = false; this.spacesShown = true; }).id('shell.spaces')
            } else {
              PhoneIconButton({symbol:Symbols.spaces,label:tr('conversation_spaces'),identity:'shell.spaces',
                action:() => { this.drawerSearching = false; this.spacesShown = true; }})
            }
          } else {
            PhoneIconButton({symbol:'navigationBack',label:tr('back'),identity:'shell.back',controlDiameter:this.phoneTop === 'session' || this.phoneToolParent ? this.sessionActionSize() : PhoneStyle.bar,action:() => this.phoneBack()})
              .bindContextMenu(this.backHistory(),ResponseType.LongPress)
          }
        },(role:string) => role)
        if (!this.compactPaletteOnRight() && this.model.profile.id) {
          this.quickPaletteButton(this.phoneTop === 'session' || this.phoneToolParent ? this.sessionActionSize() : PhoneStyle.bar)
        }
        if (this.phonePeer()) {
          Blank().layoutWeight(1)
          Stack({alignContent:Alignment.Start}) {
            Row().width(this.peerSegmentWidth()).height(this.peerSegmentHeight()).borderRadius((this.peerSegmentHeight()+8)/2)
              .backgroundColor(Colors.styleID==='beautiful' ? this.environment.dark ? '#232427' : '#FFFFFF' : Colors.control)
              .shadow({radius:Colors.styleID==='beautiful' ? 2 : 4,color:this.environment.dark ? '#33000000' : '#14000000',offsetY:1})
              .translate({x:(this.compactPeerIndex()+this.peerDrag)*this.peerSegmentWidth()})
              .animation(this.peerDrag === 0 ? Motion.state(this.environment.reduceMotion) : undefined)
              .hitTestBehavior(HitTestMode.None).accessibilityLevel('no')
            Row() {
            this.placeButton('phone_assistant_title', 0, 'shell.persona', () => this.browsePeer(0))
            Stack({alignContent: Alignment.TopEnd}) {
              this.placeButton('agents', 1, 'shell.agents', () => this.browsePeer(1))
              if (this.waitingAgentCount>0) {
                Text(this.waitingAgentCount>99 ? '99+' : String(this.waitingAgentCount)).fontSize(11).maxFontScale(1.2)
                  .fontWeight(FontWeight.Bold).fontColor(Colors.link).textAlign(TextAlign.Center)
                  .constraintSize({minWidth:17,minHeight:17}).padding({left:3,right:3}).borderRadius(9)
                  .backgroundColor(Colors.selection).offset({x:3,y:-6}).hitTestBehavior(HitTestMode.None)
                  .accessibilityLevel('no').id('shell.agents.waiting')
              }
            }
            }
          }.padding(4).borderRadius((this.peerSegmentHeight()+8)/2)
            .backgroundColor(Colors.styleID==='beautiful' ? this.environment.dark ? '#2B2C2F' : '#F2F2F3' : Colors.surface)
            .border({width:PhoneStyle.surfaceEdgeWidth(.5),color:Colors.styleID==='beautiful' ? this.environment.dark ? '#2E3033' : '#ECEDEF' : Colors.styleEdge || Colors.divider,radius:(this.peerSegmentHeight()+8)/2})
            .shadow(PhoneStyle.cardElevation(this.environment.dark))
          Blank().layoutWeight(1)
        } else if ((this.phoneTop === 'session' || this.phoneToolParent) && this.activeChild === this.childKey(this.phoneTop,this.phoneParam) && this.targetReady(this.phoneTop,this.phoneParam)) {
          PhoneSessionHeading({model:this.detailModel,space:this.phoneSessionSpace,heading:this.phonePageName(),
            onSheetChange:(shown:boolean) => this.agentReadingHeader.overlay(this.readingHeaderOwner,'info',shown)}).layoutWeight(1)
        } else if (this.phoneTop && !this.phoneLargeTitle()) {
          Text(this.phonePageName()).layoutWeight(1).fontSize(17).maxFontScale(Tokens.navigationTextMaxScale).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
            .textAlign(TextAlign.Center).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        } else Blank().layoutWeight(1)
        if (this.compactPaletteOnRight()) {
          Row({ space: 6 }) {
            if (this.model.profile.id) {
              this.quickPaletteButton(this.phonePeer() ? 40 : PhoneStyle.bar)
            }
            PhoneIconButton({symbol: this.phonePeer() || this.model.section === 0 ? Symbols.edit : Symbols.plus, label: tr(this.model.section === 0 ? 'new_chat' : 'new_agent'), identity:'shell.new', controlDiameter:this.phonePeer() ? this.peerActionSize() : PhoneStyle.bar, action:() => this.startFromPlace()})
          }.alignItems(VerticalAlign.Center)
        } else if (this.compactChatMenu(this.phoneTop) && !this.mainDestination(this.phoneTop,this.phoneParam)) {
          this.compactSessionMore()
        } else if (this.phoneTop === 'resources' && this.phoneParam === 'todo') {
          PhoneIconButton({symbol:Symbols.plus, label:tr('phone_todo_new'), tint:Colors.link, identity:'todos.add',
            action:() => { if (this.activeChild===this.childKey('resources','todo')) this.todoCreateRevision++; }})
        } else if (this.rootModuleMenuKind()) {
          ForEach([this.rootModuleMenuKind()],(kind:string) => {
            this.phoneModuleMore(kind,this.menuOwner(kind))
          },(kind:string) => this.menuOwner(kind))
        } else if (this.activeChild && this.destinationMenuItems(this.shownName, this.shownParam).length) {
          this.destinationMenus(this.shownName, this.shownParam)
        } else if (this.phoneTop && !this.phoneLargeTitle()) Blank().width(PhoneStyle.bar)
      }.width('100%').constraintSize({minHeight:50}).padding({left:16,right:16,top:this.phonePeer() || this.phoneTop === 'session' || this.phoneToolParent ? 2 : 3,
        bottom:this.phoneTop === 'session' || this.phoneToolParent ? 6 : 3}).alignItems(VerticalAlign.Center)
      if ((this.phoneTop === 'session' || this.phoneToolParent) && this.activeChild && this.targetReady(this.phoneTop,this.phoneParam) && this.detailModel.selected.machine) {
        SessionSpaceTabs({space:this.phoneSessionSpace,model:this.detailModel,
          onSheetChange:(shown:boolean) => this.agentReadingHeader.overlay(this.readingHeaderOwner,'siblings',shown),onOpenSpace:(key:string) => this.openSessionSpace(key),onPick:(key:string,from:string,id:string) => this.switchSessionTab(key,from,id)})
      }
      if (this.phoneLargeTitle()) Text(this.phonePageName()).fontSize(PhoneStyle.title).maxFontScale(Tokens.chromeFontMaxScale).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
        .width('100%').maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis}).padding({left:PhoneStyle.page,right:PhoneStyle.page,top:4,bottom:8}).id('shell.page_title')
    }.width('100%').backgroundColor(this.floatingProjectHeader || this.floatingDraftHeader ? Color.Transparent :
      (this.phonePeer() && this.model.section === 0) || this.phoneTop === 'session' || !!this.phoneToolParent ? Colors.canvas : Colors.grouped)
  }
  private parentIndexes():number[] {
    const levels:number[] = [];
    for (let i=this.stack.size()-2; i>=0; i--) levels.push(i);
    return levels;
  }
  @Builder backHistory() {
    Menu() {
      ForEach(this.parentIndexes(),(i:number) => {
        MenuItem({content:this.destinationTitleText(this.stack.getAllPathName()[i],this.stack.getParamByIndex(i) || '')})
          .onClick(() => { if (i < this.stack.size()-1) { this.stack.popToIndex(i); this.savePath(); } }).id('shell.back.level')
      },(i:number) => String(i))
      if (this.model.section === 1 || this.model.section === 4) MenuItem({content:tr(this.model.section === 4 ? 'projects' : 'agents')})
        .onClick(() => { this.stack.clear(); this.savePath(); }).id('shell.back.root')
    }
  }
  private selectPeer(index: number): void {
    if (index === this.model.section) return;
    this.spacesShown = false; this.changeSection(index);
    if (index === 0) this.ensureMainDestination();
  }
  private ensureAgentWorkspace(): void {
    if (!this.agentWorkspace.isBoundTo(this.model)) this.agentWorkspace.open(this.model, this.peer() === 1).catch(() => {});
  }
  @Computed get waitingAgentCount():number {
    const bound=this.agentWorkspace.isBoundTo(this.model);
    return this.model.directoryOwners().reduce((total:number,owner:ClientModel) =>
      total+owner.phoneAgentBadge((row:ResourceRow, space:string) => bound && this.agentWorkspace.isMuted(row,space)),0);
  }
  @Monitor('model.ready', 'model.mainId', 'model.mainMachineId', 'model.connected') mainAvailabilityChanged(): void {
    this.ensureMainDestination();
    if (this.model.ready && this.model.connected && !this.wide()) this.ensureAgentWorkspace();
  }
  private ensureMainDestination(): void {
    if (!this.model.ready || !this.model.connected || this.model.section !== 0 || this.navigationRestoredScope !== this.model.scope() || this.showSheet || this.spacesShown || this.stack.size() || this.defaultMainPending || (this.creatingConversations.get(JSON.stringify([this.model.scope(), this.model.connectionGeneration()])) || 0) > 0) return;
    const attempt = JSON.stringify([this.model.scope(), this.model.connectionGeneration(), this.model.mainId, this.model.mainMachineId]);
    if (attempt === this.defaultMainAttempt) return;
    this.defaultMainAttempt = attempt; this.resolveMainDestination();
  }
  private async resolveMainDestination(): Promise<void> {
    if (this.defaultMainPending || !this.model.connected) return;
    const owner = this.model, repo = owner.repo(), generation = owner.connectionGeneration(), scope = owner.scope();
    const stack = this.stack, revision = this.routeRevision, selected = owner.selected.key(), section = owner.section, intent = this.navigationIntent;
    const names = stack.getAllPathName(), pathStamp = JSON.stringify(names.map((name: string, index: number) => [name, stack.getParamByIndex(index)]));
    const valid = (): boolean => this.model === owner && owner.connected && owner.owns(generation, scope) && owner.repo() === repo && this.stack === stack &&
      this.routeRevision === revision && this.navigationIntent === intent && !this.showSheet && !this.spacesShown && owner.section === section && owner.selected.key() === selected &&
      JSON.stringify(stack.getAllPathName().map((name: string, index: number) => [name, stack.getParamByIndex(index)])) === pathStamp;
    this.defaultMainPending = true;
    try {
      const response = await repo.call('/api/v1/assistant');
      if (!valid()) return;
      const data = mainConversationData(response);
      if (!data) return;
      const row = ResourceRow.from(data as Object as Record<string, Object>, 'conversation');
      row.group = owner.profile.id; row.manager = owner.profile.manager;
      if (!valid()) return;
      owner.mainId = row.id; owner.mainMachineId = row.machine;
      this.pushChat(row);
      this.defaultMainAttempt = JSON.stringify([owner.scope(), generation, owner.mainId, owner.mainMachineId]);
    } catch (error) { if (valid()) owner.reportError(error as Error); }
    finally { this.defaultMainPending = false; const waiting = this.mainReadWaiters; this.mainReadWaiters = []; waiting.forEach((resolve: () => void) => resolve()); if (scope !== this.model.scope() || generation !== this.model.connectionGeneration()) this.ensureMainDestination(); }
  }
  private openAssistantCard(itemId: string, kind: string): void {
    const source = this.detailModel.selected;
    if (!itemId || !this.activeChild || !['conversation', 'session'].includes(source.kind)) return;
    const row = NavigationContext.rowFrom(JSON.stringify(source));
    const data = row.data(); data['assistant_item_id'] = itemId; data['assistant_item_kind'] = kind; row.raw = JSON.stringify(data);
    this.stack.pushPath({ name: 'assistant', param: JSON.stringify(row) }); this.savePath();
  }
  @Computed get foldLayout(): FoldAdaptiveLayout {
    const widthVp = this.foldWindowWidthVp > 0 ? this.foldWindowWidthVp : this.phoneWidth;
    const heightVp = this.foldWindowHeightVp > 0 ? this.foldWindowHeightVp : this.phoneHeight;
    return resolveFoldLayout({
      widthClass: widthClassFromBreakpoint(Number(this.breakpoint)),
      windowWidthVp: widthVp,
      windowHeightVp: heightVp,
      isFoldable: this.environment.isFoldable,
      foldStatus: this.environment.foldStatus,
      foldDisplayMode: this.environment.foldDisplayMode,
      nativeHoverAvailable: this.environment.nativeHoverAvailable
    });
  }
  @Computed get hoverSplit(): HoverSplitGeometry {
    if (!this.foldLayout.hoverMode || this.environment.creaseHeightPx <= 0) {
      return { active: false, topPaneHeightVp: 0, creaseBandVp: 0, bottomPaneHeightVp: 0 };
    }
    const heightVp = this.foldWindowHeightVp > 0 ? this.foldWindowHeightVp : this.phoneHeight;
    let creaseTopVp = 0;
    let creaseHeightVp = 0;
    try {
      const ui = this.getUIContext();
      creaseTopVp = ui.px2vp(this.environment.creaseTopPx);
      creaseHeightVp = ui.px2vp(this.environment.creaseHeightPx);
    } catch (_) {
      return { active: false, topPaneHeightVp: 0, creaseBandVp: 0, bottomPaneHeightVp: 0 };
    }
    return resolveHoverSplitGeometry(heightVp, creaseTopVp, creaseHeightVp);
  }
  private openXiaoYiQuick(): void {
    if (this.wide() && this.model.profile.id) {
      this.openSettings('phone_xiaoyi');
    } else {
      this.phoneSettingsQuery = '';
      this.phoneSettingsTrail = pushPhoneSetting([], 'phone_xiaoyi');
      if (this.model.profile.id && !this.wide()) this.spacesShown = true;
      this.phoneSettingsShown = true;
    }
  }
  private async interruptSessionFromWorkbench(row: ResourceRow): Promise<void> {
    if (row.kind !== 'session') return;
    const rowKey = row.key();
    if (!rowKey || this.workbenchInterruptKey === rowKey) return;
    this.workbenchInterruptKey = rowKey;
    let owner: ClientModel = this.model;
    try {
      owner = row.manager || (row.group && row.group !== this.model.profile.id)
        ? await this.model.viewFor(row)
        : this.model;
      const generation = owner.connectionGeneration();
      const scope = owner.scope();
      const sessionId = row.session || row.id;
      if (!owner.owns(generation, scope) || !owner.canInterruptSession(row)) return;
      if (owner.selected.key() === rowKey && owner.canStop()) {
        const stopped = await owner.stop((): boolean => owner.owns(generation, scope));
        if (stopped && owner.owns(generation, scope)) {
          toastCenter.show(tr('fold_interrupt_agent'), 'info');
        } else if (!stopped && owner.owns(generation, scope) && owner.error) {
          toastCenter.error(trError(owner.error));
        }
        return;
      }
      const path = '/api/v1/sessions/' + encodeURIComponent(row.machine) + '/' + encodeURIComponent(sessionId) + '/interrupt';
      if (owner.operationBlocked(path)) return;
      const admit = (): boolean => owner.owns(generation, scope) && owner.connected && owner.canInterruptSession(row);
      await owner.repo().call(path, 'POST', Wire.payload([], []), '', admit);
      if (!owner.owns(generation, scope)) return;
      await owner.refresh();
      if (!owner.owns(generation, scope)) return;
      toastCenter.show(tr('fold_interrupt_agent'), 'info');
    } catch (error) {
      owner.reportError(error as Error);
      toastCenter.error(trError(owner.error || (error as Error).message || 'failed'));
    } finally {
      if (this.workbenchInterruptKey === rowKey) {
        this.workbenchInterruptKey = '';
      }
    }
  }
  private wide(): boolean {
    return this.breakpoint === WidthBreakpoint.WIDTH_LG || this.breakpoint === WidthBreakpoint.WIDTH_XL || this.foldLayout.navigationMode === 'split';
  }
  private rail(): boolean { return this.wide() && this.collapsed; }
  private showFoldInspector(): boolean {
    if (!this.wide() || !this.foldLayout.supportsInspector || !this.model.profile.id) return false;
    return this.foldInspectorUserToggled ? this.foldInspectorOpen : this.foldLayout.autoShowInspector;
  }
  private setFoldInspectorOpen(next: boolean): void {
    Motion.run(this.getUIContext(), Motion.layout(this.environment.reduceMotion), () => {
      this.foldInspectorUserToggled = true;
      this.foldInspectorOpen = next;
      if (next && this.foldLayout.collapseRailForInspector && !this.collapsed) {
        this.foldInspectorCollapsedRail = true;
        this.collapsed = true;
      } else if (!next && this.foldInspectorCollapsedRail) {
        this.foldInspectorCollapsedRail = false;
        this.collapsed = false;
      }
    });
  }
  private toggleFoldInspector(): void {
    Haptics.selection();
    this.setFoldInspectorOpen(!this.showFoldInspector());
  }
  private foldPostureLabel(): string {
    const p = this.foldLayout.posture;
    if (p === 'trifold_unfolded') return tr('fold_posture_trifold');
    if (p === 'trifold_angled') return tr('fold_posture_trifold_angled');
    if (p === 'book_half_open') return tr('fold_posture_book');
    if (p === 'dual_unfolded') return tr('fold_posture_dual');
    if (p === 'hover_tabletop') return tr('fold_posture_hover');
    return '';
  }
  private isTriFoldPosture(): boolean {
    return this.foldLayout.posture === 'trifold_unfolded' ||
      this.foldLayout.posture === 'trifold_angled';
  }
  private stepFoldRelay(direction: 1 | -1): void {
    const rows = (this.model.directoryReady && this.model.directorySessions.length
      ? this.model.directorySessions
      : this.model.sessions).filter((row: ResourceRow) => {
        if (row.kind !== 'session') return false;
        const data = row.data();
        return !Wire.bool(data, 'archived') && !Wire.bool(data, 'assistant') && !Wire.bool(data, 'hidden_by_directory');
      });
    const states = rows.map((row: ResourceRow) => {
      const data = row.data();
      const raw = row.state || Wire.text(data, 'status');
      if (Wire.bool(data, 'needs_you') || raw === 'needs_you' || raw === 'blocked') return 'needs_you';
      if (Wire.bool(data, 'working') || raw === 'working' || raw === 'running') return 'working';
      if (raw === 'done' || raw === 'unread' || Wire.bool(data, 'unread')) return 'unread';
      return 'idle';
    });
    const currentKey = this.detailModel.selected?.key() || this.model.selected?.key() || '';
    const currentIndex = rows.findIndex((row: ResourceRow) => row.key() === currentKey);
    const nextIndex = pickRelaySessionIndex(states, currentIndex, direction);
    if (nextIndex >= 0 && rows[nextIndex]) {
      Haptics.selection();
      this.openPickedChat(rows[nextIndex]);
    }
  }
  private expandRail(): void {
    Haptics.tap();
    Motion.run(this.getUIContext(), Motion.layout(this.environment.reduceMotion), () => {
      this.collapsed = false;
      if (this.foldInspectorCollapsedRail || (this.foldLayout.collapseRailForInspector && this.showFoldInspector())) {
        this.foldInspectorCollapsedRail = false;
        this.foldInspectorUserToggled = true;
        this.foldInspectorOpen = false;
      }
    });
  }
  private keyNameForShortcut(event: KeyEvent): string {
    if (event.keyCode === KeyCode.KEYCODE_ESCAPE) return 'escape';
    if (event.keyCode === KeyCode.KEYCODE_K) return 'k';
    if (event.keyCode === KeyCode.KEYCODE_N) return 'n';
    if (event.keyCode === KeyCode.KEYCODE_I) return 'i';
    if (event.keyCode === KeyCode.KEYCODE_1) return '1';
    if (event.keyCode === KeyCode.KEYCODE_2) return '2';
    if (event.keyCode === KeyCode.KEYCODE_3) return '3';
    if (event.keyCode === KeyCode.KEYCODE_0) return '0';
    if (event.keyCode === KeyCode.KEYCODE_LEFT_BRACKET) return '[';
    if (event.keyCode === KeyCode.KEYCODE_RIGHT_BRACKET) return ']';
    return '';
  }
  private isCtrlOrMetaPressed(event: KeyEvent): boolean {
    if (event.metaKey > 0) return true;
    try {
      if (event.getModifierKeyState && event.getModifierKeyState(['Ctrl'])) return true;
    } catch (_) {}
    return false;
  }
  private hasCompetingModalOverlay(): boolean {
    return this.phoneSettingsShown ||
      this.activityVisible ||
      this.readerShown ||
      this.directionPermissionsVisible ||
      this.spacesShown ||
      this.showSheet ||
      this.chatForward.isChoosing(this.detailModel) ||
      this.starredSheetShown ||
      this.exportSheetShown ||
      this.archivedSheetShown ||
      this.standingAuthSheetShown ||
      this.fullSearchSheetShown ||
      this.autoChatsSheetShown ||
      this.tokenUsageOpen ||
      !!this.messageLift.current;
  }
  private openQuickPalette(): void {
    if (!this.model.profile.id || this.hasCompetingModalOverlay()) return;
    if (this.chatSearchShown) this.chatSearchShown = false;
    if (this.chatTitleMenuVisible || this.chatBodyMenuVisible) this.dismissChatActions();
    Haptics.selection();
    this.quickPaletteOpen = true;
  }
  private async executeQuickCommand(entry: QuickCommandEntry): Promise<void> {
    this.quickPaletteOpen = false;
    if (entry.category === 'agents') {
      const rows = this.model.directoryReady && this.model.directorySessions.length
        ? this.model.directorySessions
        : this.model.sessions;
      const target = rows.find((row: ResourceRow) =>
        (entry.sessionKey && row.key() === entry.sessionKey) || (entry.sessionId && row.id === entry.sessionId)
      );
      if (target) this.openPickedChat(target);
      return;
    }
    const id = entry.id;
    if (id === 'workspace:persona') {
      this.openSpace('persona');
    } else if (id === 'workspace:agents') {
      this.openSpace('agents');
    } else if (id === 'workspace:inbox') {
      if (this.model.inboxPageVisible()) this.openSpace('inbox');
    } else if (id === 'workspace:todos') {
      if (this.model.todosPageVisible()) this.openSpace('todo');
    } else if (id === 'workspace:knowledge') {
      if (this.model.knowledgePageVisible()) this.openSpace('knowledge');
    } else if (id === 'workspace:health') {
      this.openSpace('health');
    } else if (id === 'workspace:machines') {
      this.openSpace('devices');
    } else if (id === 'workspace:settings') {
      this.openSpace('settings');
    } else if (id === 'action:toggle_workbench') {
      if (this.wide() && this.foldLayout.supportsInspector) this.toggleFoldInspector();
    } else if (id === 'action:workbench_code') {
      if (this.wide() && this.foldLayout.supportsInspector) this.switchFoldWorkbenchTab(1);
    } else if (id === 'action:workbench_machines') {
      if (this.wide() && this.foldLayout.supportsInspector) this.switchFoldWorkbenchTab(2);
    } else if (id === 'action:open_token_usage') {
      this.tokenUsageOpen = true;
    } else if (id === 'action:open_xiaoyi') {
      this.openXiaoYiQuick();
    } else if (id === 'action:toggle_theme') {
      const nextMode = this.environment.dark ? 1 : 2;
      try {
        const saved = await this.model.setAppearance(nextMode);
        if (saved && this.model.appearance === nextMode) {
          this.environment.dark = nextMode === 2;
        }
      } catch (_) {}
    } else if (id === 'action:liveview_preview') {
      nativeLiveView.publishSample('needsYou').then((ok: boolean) => {
        if (ok) {
          Haptics.success();
          toastCenter.success(tr('live_view_published_toast'));
        }
      }).catch(() => {});
      this.openSettings('notifications');
    } else if (id === 'action:open_starred') {
      this.starredSheetShown = true;
    } else if (id === 'action:open_archived') {
      this.archivedSheetShown = true;
    } else if (id === 'action:export_chat') {
      this.openChatExport();
    } else if (id === 'action:open_standing_auth') {
      this.standingAuthSheetShown = true;
    } else if (id === 'action:open_full_search') {
      this.fullSearchSheetShown = true;
    } else if (id === 'action:open_all_chats') {
      this.autoChatsSheetShown = true;
    }
  }
  @Builder quickPaletteButton(diameter: number = PhoneStyle.bar, compactWide: boolean = false) {
    Button({ type: ButtonType.Circle }) {
      SymbolGlyph(Symbols.search)
        .fontSize(compactWide ? Tokens.symbolFont : String(Math.round(18 * diameter / PhoneStyle.bar)) + 'vp')
        .fontColor([this.quickPaletteOpen ? Colors.link : Colors.ink])
    }
    .width(diameter)
    .height(diameter)
    .padding(0)
    .responseRegion({
      x: diameter < 44 ? (diameter - 44) / 2 : 0,
      y: diameter < 44 ? (diameter - 44) / 2 : 0,
      width: Math.max(44, diameter),
      height: Math.max(44, diameter)
    })
    .backgroundColor(
      this.quickPaletteOpen
        ? Colors.selection
        : Colors.styleID === 'glass'
          ? Colors.elevatedGlass
          : Colors.surface
    )
    .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
    .border({
      width: PhoneStyle.surfaceEdgeWidth(0.5),
      color: this.quickPaletteOpen ? Colors.link : PhoneStyle.floatingBorder(this.environment.dark),
      radius: diameter / 2
    })
    .shadow(PhoneStyle.floatingElevation(this.environment.dark))
    .accessibilityText(tr('quick_palette_title'))
    .bindTips(tr('quick_palette_title'))
    .onClick(() => this.openQuickPalette())
    .id('shell.quick.palette')
  }
  @Builder quickPaletteSheet() {
    WithTheme({ theme: nativeLookTheme(this.look, this.model.style, this.environment.dark), colorMode: this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT }) {
      ThemeScope({ systemControls: this.look.accentId === 'style' && usesSystemStyleColors(this.model.style),
        styleID: phoneInterfaceStyleID(this.model.style),
        interfaceAccent: deviceLookInterfaceTint(this.look.accentId,this.model.style,this.environment.dark,
          this.environment.dark ? this.look.palette(this.model.style).dark : this.look.palette(this.model.style).light),
        surfaces: deviceLookSurface(this.model.style, this.look.accentId, this.environment.dark, this.look.palette(this.model.style)),
        selectedAccentInk: deviceLookSelectedFilterInk(this.look.palette(this.model.style), this.environment.dark) }) {
        QuickCommandSheet({
          model: this.model,
          showWorkbench: this.wide() && this.foldLayout.supportsInspector,
          showLiveView: true,
          showExportChat: this.detailModel.isChat() && this.detailModel.messages.length > 0,
          autoFocus: this.wide(),
          onSelectEntry: (entry: QuickCommandEntry) => this.executeQuickCommand(entry),
          onClose: () => { this.quickPaletteOpen = false; }
        })
      }
    }
  }
  private openChatExport(): void {
    if (!this.detailModel.isChat()) return;
    this.exportSheetShown = true;
  }
  private openStarredOriginal(star: StarredSnapshot): void {
    this.starredSheetShown = false;
    const pool = star.conversation === 'agent' ? this.model.sessions : this.model.conversations;
    const existing = pool.find((row: ResourceRow) =>
      row.machine === star.machine_id && (row.session || row.id) === star.session_id
    );
    if (existing) {
      this.openPickedChat(existing, star.item_id);
      return;
    }
    const kind = star.conversation === 'agent' ? 'session' : 'conversation';
    const data = Wire.payload(
      ['id', 'session_id', 'machine_id', 'manager', 'title'],
      [star.session_id, star.session_id, star.machine_id, star.source_manager || this.model.profile.manager, star.title || tr('conversation')]
    );
    const fallbackRow = ResourceRow.from(data, kind);
    fallbackRow.group = this.model.profile.id;
    fallbackRow.manager = star.source_manager || this.model.profile.manager;
    fallbackRow.machine = star.machine_id;
    fallbackRow.session = star.session_id;
    this.openPickedChat(fallbackRow, star.item_id);
  }
  private async forkCurrentChat(): Promise<void> {
    const owner = this.detailModel, row = owner.selected;
    const machine = row.machine, sid = row.session || row.id;
    if (!owner.connected || !owner.isChat() || !machine || !sid) return;
    const isMain = row.kind === 'conversation' && sid === owner.mainId && (!owner.mainMachineId || machine === owner.mainMachineId);
    if (isMain || (!owner.demo && !Wire.bool(row.data(), 'can_fork'))) return;
    const path = '/api/v1/sessions/' + encodeURIComponent(machine) + '/' + encodeURIComponent(sid) + '/fork';
    if (owner.operationBlocked(path)) {
      toastCenter.error(tr('pending_action'));
      return;
    }
    const busyKey = JSON.stringify([owner.scope(), owner.connectionGeneration(), machine, sid]);
    if (this.forkSessionTarget === busyKey) return;
    const generation = owner.connectionGeneration(), scope = owner.scope(), selectedKey = row.key();
    const admit = (): boolean => this.detailModel === owner && owner.owns(generation, scope) && owner.selected.key() === selectedKey;
    this.forkSessionTarget = busyKey;
    try {
      const response = await owner.repo().call(path, 'POST', {}, util.generateRandomUUID(), admit);
      if (!this.detailModel || this.detailModel !== owner || !owner.owns(generation, scope)) return;
      const forkedId = Wire.text(response, 'session_id');
      if (!forkedId || forkedId === sid) {
        toastCenter.error(tr('result_unknown'));
        return;
      }
      const data = Wire.payload(
        ['id', 'session_id', 'machine_id', 'manager', 'title', 'cwd', 'status', 'state', 'can_fork'],
        [forkedId, forkedId, machine, owner.profile.manager, Wire.text(response, 'title') || row.title, row.path, 'idle', 'idle', true]
      );
      const forkedRow = ResourceRow.from(data, row.kind);
      forkedRow.group = row.group || owner.profile.id;
      forkedRow.manager = row.manager || owner.profile.manager;
      forkedRow.machine = machine;
      forkedRow.session = forkedId;
      forkedRow.path = row.path;
      Haptics.success();
      toastCenter.success(tr('fork_chat_done_toast'));
      owner.refresh().catch(() => {});
      this.openPickedChat(forkedRow);
    } catch (error) {
      if (this.detailModel === owner && owner.owns(generation, scope)) {
        owner.reportError(error as Error);
        Haptics.error();
        toastCenter.error(trError(owner.error || 'failed'));
      }
    } finally {
      if (this.forkSessionTarget === busyKey) this.forkSessionTarget = '';
    }
  }
  @Builder starredSheetContent() {
    WithTheme({ theme: nativeLookTheme(this.look, this.model.style, this.environment.dark), colorMode: this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT }) {
      ThemeScope({ systemControls: this.look.accentId === 'style' && usesSystemStyleColors(this.model.style),
        styleID: phoneInterfaceStyleID(this.model.style),
        interfaceAccent: deviceLookInterfaceTint(this.look.accentId,this.model.style,this.environment.dark,
          this.environment.dark ? this.look.palette(this.model.style).dark : this.look.palette(this.model.style).light),
        surfaces: deviceLookSurface(this.model.style, this.look.accentId, this.environment.dark, this.look.palette(this.model.style)),
        selectedAccentInk: deviceLookSelectedFilterInk(this.look.palette(this.model.style), this.environment.dark) }) {
        StarredMessagesSheet({
          model: this.model,
          onOpenOriginal: (star: StarredSnapshot) => this.openStarredOriginal(star),
          onClose: () => { this.starredSheetShown = false; }
        })
      }
    }
  }
  @Builder exportSheetContent() {
    WithTheme({ theme: nativeLookTheme(this.look, this.model.style, this.environment.dark), colorMode: this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT }) {
      ThemeScope({ systemControls: this.look.accentId === 'style' && usesSystemStyleColors(this.model.style),
        styleID: phoneInterfaceStyleID(this.model.style),
        interfaceAccent: deviceLookInterfaceTint(this.look.accentId,this.model.style,this.environment.dark,
          this.environment.dark ? this.look.palette(this.model.style).dark : this.look.palette(this.model.style).light),
        surfaces: deviceLookSurface(this.model.style, this.look.accentId, this.environment.dark, this.look.palette(this.model.style)),
        selectedAccentInk: deviceLookSelectedFilterInk(this.look.palette(this.model.style), this.environment.dark) }) {
        ChatExportSheet({
          model: this.detailModel,
          onClose: () => { this.exportSheetShown = false; }
        })
      }
    }
  }
  @Builder archivedSheetContent() {
    WithTheme({ theme: nativeLookTheme(this.look, this.model.style, this.environment.dark), colorMode: this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT }) {
      ThemeScope({ systemControls: this.look.accentId === 'style' && usesSystemStyleColors(this.model.style),
        styleID: phoneInterfaceStyleID(this.model.style),
        interfaceAccent: deviceLookInterfaceTint(this.look.accentId,this.model.style,this.environment.dark,
          this.environment.dark ? this.look.palette(this.model.style).dark : this.look.palette(this.model.style).light),
        surfaces: deviceLookSurface(this.model.style, this.look.accentId, this.environment.dark, this.look.palette(this.model.style)),
        selectedAccentInk: deviceLookSelectedFilterInk(this.look.palette(this.model.style), this.environment.dark) }) {
        ArchivedConversationsSheet({
          model: this.model,
          onOpen: (row: ResourceRow) => {
            this.archivedSheetShown = false;
            this.openPickedChat(row);
          },
          onClose: () => { this.archivedSheetShown = false; }
        })
      }
    }
  }
  @Builder standingAuthSheetContent() {
    WithTheme({ theme: nativeLookTheme(this.look, this.model.style, this.environment.dark), colorMode: this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT }) {
      ThemeScope({ systemControls: this.look.accentId === 'style' && usesSystemStyleColors(this.model.style),
        styleID: phoneInterfaceStyleID(this.model.style),
        interfaceAccent: deviceLookInterfaceTint(this.look.accentId,this.model.style,this.environment.dark,
          this.environment.dark ? this.look.palette(this.model.style).dark : this.look.palette(this.model.style).light),
        surfaces: deviceLookSurface(this.model.style, this.look.accentId, this.environment.dark, this.look.palette(this.model.style)),
        selectedAccentInk: deviceLookSelectedFilterInk(this.look.palette(this.model.style), this.environment.dark) }) {
        StandingAuthSheet({
          model: this.model,
          onClose: () => { this.standingAuthSheetShown = false; }
        })
      }
    }
  }
  @Builder fullSearchSheetContent() {
    WithTheme({ theme: nativeLookTheme(this.look, this.model.style, this.environment.dark), colorMode: this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT }) {
      ThemeScope({ systemControls: this.look.accentId === 'style' && usesSystemStyleColors(this.model.style),
        styleID: phoneInterfaceStyleID(this.model.style),
        interfaceAccent: deviceLookInterfaceTint(this.look.accentId,this.model.style,this.environment.dark,
          this.environment.dark ? this.look.palette(this.model.style).dark : this.look.palette(this.model.style).light),
        surfaces: deviceLookSurface(this.model.style, this.look.accentId, this.environment.dark, this.look.palette(this.model.style)),
        selectedAccentInk: deviceLookSelectedFilterInk(this.look.palette(this.model.style), this.environment.dark) }) {
        AssistantSearchSheet({
          model: this.model,
          onOpen: (row: ResourceRow, itemId: string) => {
            this.fullSearchSheetShown = false;
            this.openPickedChat(row, itemId);
          },
          onClose: () => { this.fullSearchSheetShown = false; }
        })
      }
    }
  }
  @Builder autoChatsSheetContent() {
    WithTheme({ theme: nativeLookTheme(this.look, this.model.style, this.environment.dark), colorMode: this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT }) {
      ThemeScope({ systemControls: this.look.accentId === 'style' && usesSystemStyleColors(this.model.style),
        styleID: phoneInterfaceStyleID(this.model.style),
        interfaceAccent: deviceLookInterfaceTint(this.look.accentId,this.model.style,this.environment.dark,
          this.environment.dark ? this.look.palette(this.model.style).dark : this.look.palette(this.model.style).light),
        surfaces: deviceLookSurface(this.model.style, this.look.accentId, this.environment.dark, this.look.palette(this.model.style)),
        selectedAccentInk: deviceLookSelectedFilterInk(this.look.palette(this.model.style), this.environment.dark) }) {
        AutoConversationsSheet({
          model: this.model,
          history: this.sidebarTaskHistory,
          onOpen: (row: ResourceRow) => {
            this.autoChatsSheetShown = false;
            this.openPickedChat(row);
          },
          onClose: () => { this.autoChatsSheetShown = false; }
        })
      }
    }
  }
  @Builder tokenUsageSheet() {
    WithTheme({ theme: nativeLookTheme(this.look, this.model.style, this.environment.dark), colorMode: this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT }) {
      ThemeScope({ systemControls: this.look.accentId === 'style' && usesSystemStyleColors(this.model.style),
        styleID: phoneInterfaceStyleID(this.model.style),
        interfaceAccent: deviceLookInterfaceTint(this.look.accentId,this.model.style,this.environment.dark,
          this.environment.dark ? this.look.palette(this.model.style).dark : this.look.palette(this.model.style).light),
        surfaces: deviceLookSurface(this.model.style, this.look.accentId, this.environment.dark, this.look.palette(this.model.style)),
        selectedAccentInk: deviceLookSelectedFilterInk(this.look.palette(this.model.style), this.environment.dark) }) {
        TokenUsageSheet({
          model: this.model,
          onClose: () => { this.tokenUsageOpen = false; }
        })
      }
    }
  }
  private dismissTopOverlay(): boolean {
    if (this.exportSheetShown) { this.exportSheetShown = false; return true; }
    if (this.starredSheetShown) { this.starredSheetShown = false; return true; }
    if (this.archivedSheetShown) { this.archivedSheetShown = false; return true; }
    if (this.standingAuthSheetShown) { this.standingAuthSheetShown = false; return true; }
    if (this.fullSearchSheetShown) { this.fullSearchSheetShown = false; return true; }
    if (this.autoChatsSheetShown) { this.autoChatsSheetShown = false; return true; }
    if (this.tokenUsageOpen) { this.tokenUsageOpen = false; return true; }
    if (this.quickPaletteOpen) { this.quickPaletteOpen = false; return true; }
    if (this.directionPermissionsVisible) { this.directionPermissionsVisible = false; return true; }
    if (this.messageLift.dismiss() || this.closeForwardNavigation()) return true;
    if (this.chatTitleMenuVisible || this.chatBodyMenuVisible) { this.dismissChatActions(); return true; }
    if (this.chatSearchShown) { this.chatSearchShown = false; return true; }
    if (this.readerShown) { this.readerShown = false; return true; }
    if (this.activityVisible) { this.activityVisible = false; return true; }
    if (this.showSheet) {
      if (this.sheetKind === 'newagent' && this.agentPanel) this.closeAgentPanel();
      else if (!this.creatingAgent) this.showSheet = false;
      return true;
    }
    if (this.phoneSettingsShown) {
      if (this.phoneSettingsPage) this.backPhoneSetting();
      else { this.phoneSettingsQuery = ''; this.phoneSettingsShown = false; }
      return true;
    }
    if (this.spacesShown) { this.spacesShown = false; return true; }
    if (this.showFoldInspector()) { this.setFoldInspectorOpen(false); return true; }
    return false;
  }
  private switchFoldWorkbenchTab(tab: number): void {
    Haptics.selection();
    if (!this.showFoldInspector()) this.setFoldInspectorOpen(true);
    this.foldWorkbenchTab = tab;
    this.foldWorkbenchTabSignal++;
  }
  private handleFoldKeyEvent(event: KeyEvent): void {
    if (event.type !== KeyType.Down || !this.model.profile.id) return;
    const keyName = this.keyNameForShortcut(event);
    const ctrlOrMeta = this.isCtrlOrMetaPressed(event);
    const supportsInspector = this.wide() && this.foldLayout.supportsInspector;
    const legacyAction = resolveFoldShortcut(keyName, ctrlOrMeta, supportsInspector);
    const action = resolveFoldKeyboardShortcut(keyName, ctrlOrMeta, supportsInspector);
    if (action === 'none' && legacyAction === 'none') return;
    if (action === 'open_palette') {
      this.openQuickPalette();
      event.stopPropagation();
    } else if (action === 'search') {
      Haptics.selection();
      this.showAllChats(this.peer() === 1 || this.projectPlace());
      event.stopPropagation();
    } else if (action === 'new_chat') {
      Haptics.tap();
      this.startFromPlace();
      event.stopPropagation();
    } else if (action === 'toggle_inspector') {
      this.toggleFoldInspector();
      event.stopPropagation();
    } else if (action === 'workbench_pulse') {
      this.switchFoldWorkbenchTab(0);
      event.stopPropagation();
    } else if (action === 'workbench_code') {
      this.switchFoldWorkbenchTab(1);
      event.stopPropagation();
    } else if (action === 'workbench_machines') {
      this.switchFoldWorkbenchTab(2);
      event.stopPropagation();
    } else if (action === 'open_xiaoyi') {
      Haptics.tap();
      this.openXiaoYiQuick();
      event.stopPropagation();
    } else if (action === 'next_unread') {
      this.stepFoldRelay(1);
      event.stopPropagation();
    } else if (action === 'prev_unread') {
      this.stepFoldRelay(-1);
      event.stopPropagation();
    } else if (action === 'dismiss') {
      if (this.dismissTopOverlay()) event.stopPropagation();
    }
  }
  private filtered(): ResourceRow[] {
    return this.model.sessions.filter((row: ResourceRow) =>
      (!this.query || (row.title + ' ' + row.path + ' ' + row.machine).toLowerCase().includes(this.query.toLowerCase())) &&
      (this.filter === 0 || this.filter === 1 && row.state === 'needs_you' || this.filter === 2 && row.state === 'working'));
  }
  private hideAgentTitle(): boolean {
    const epoch = this.visibilityEpoch;
    const ratio = this.rowVisibility.get(this.model.scope() + this.model.selected.key()) || 0;
    return this.detailModel === this.model && this.model.section === 1 && this.wide() && !this.rail() && this.model.selected.kind === 'session' && ratio >= 0.99;
  }
  private openReader(address: string): void { const safe = readerLink(address); if (safe) { this.readerAddress = safe; this.readerTitle = readerHost(safe); this.readerShown = true; } }
  @Builder readerContent() { LinkReader({ address: this.readerAddress, onHost: (host: string) => this.readerTitle = host }) }
  private openSheet(kind: string, connectionStart: string = 'password', pairingAddress: string = ''): void {
    this.navigationIntent++;
    if (kind === 'connect') {
      this.pairingRevision++;
      this.scanRevision++;
      this.clearPairTransport();
      this.scanning = false;
      this.address = pairingAddress;
      this.pairCode = '';
      this.model.error = '';
      this.pairManually = connectionStart === 'manual';
      this.pairByPassword = connectionStart === 'password';
      this.passwordAuthorizing = false;
      phonePasswordLogin.cancel();
    }
    this.readerShown = false;
    if (kind === 'newagent') { this.prepareAgent(); return; }
    this.sheetKind = kind;
    this.showSheet = true;
  }
  private compactPairing(): boolean { return this.sheetKind === 'connect' && !this.wide(); }
  private sheetTitle(): string { return tr(this.sheetKind === 'hub' ? 'hub' : this.sheetKind === 'newagent' ? 'new_agent' : 'welcome'); }
  @Builder sheetContent() {
    if (this.sheetKind === 'hub') this.workspaceSheet()
    else if (this.sheetKind === 'newagent') this.newAgentForm()
    else if (this.compactPairing() && this.editingPairSsh) {
      PairingSshForm({ draft: this.pairSsh, hubAddress: this.address, onDone: () => { this.editingPairSsh = false; },
        onClose: () => { this.editingPairSsh = false; } })
    } else if (this.compactPairing()) {
      PhonePairingForm({ password: this.pairByPassword, manual: this.pairManually, address: this.address,
        code: this.pairCode, busy: this.model.busy || this.scanning || this.confirmingScan || this.passwordAuthorizing, error: this.model.error ? trError(this.model.error) : '',
        accessExpanded: this.pairAccessExpanded, accessID: this.pairAccessID,
        accessSecret: this.pairAccessSecret, sshLabel: this.pairSsh.label, sshPolicy: this.pairSsh.configuration.routePolicy,
        onAccessExpanded: (value: boolean) => { this.pairAccessExpanded = value; },
        onAccessID: (value: string) => { this.pairAccessID = value; this.pairSsh.cancelAttempt(); },
        onAccessSecret: (value: string) => { this.pairAccessSecret = value; this.pairSsh.cancelAttempt(); },
        onSsh: () => { this.pairSsh.cancelAttempt(); this.editingPairSsh = true; },
        onAddress: (value: string) => { this.address = value; this.pairSsh.cancelAttempt(); }, onCode: (value: string) => { this.pairCode = value; },
        onPassword: (value: boolean) => {
          if (this.pairByPassword !== value) {
            this.pairingRevision++; this.scanRevision++; phonePasswordLogin.cancel();
            this.pairByPassword = value; this.pairSsh.cancelAttempt(); this.model.error = ''; this.passwordAuthorizing = false; this.scanning = false; this.confirmingScan = false;
          }
        },
        onManual: () => { this.scanRevision++; this.confirmingScan = false; this.scanning = false; this.pairManually = true; },
        onPhoto: () => this.choosePairingPhoto(), onScanned: (value: string) => this.confirmScannedPairing(value),
        onClose: () => { this.showSheet = false; },
        onConnect: () => { if (this.pairByPassword) this.passwordConnect(); else this.connect(); } })
    } else this.pairingForm()
  }
  @Monitor('environment.foldStatus', 'environment.foldDisplayMode', 'environment.windowWidthPx', 'environment.windowHeightPx')
  foldEnvironmentChanged(): void {
    this.updateBreakpoint();
  }
  private updateBreakpoint(): void {
    const oldWide = this.wide();
    const ui = this.getUIContext();
    this.breakpoint = ui.getWindowWidthBreakpoint();
    if (this.environment.windowWidthPx > 0) this.foldWindowWidthVp = ui.px2vp(this.environment.windowWidthPx);
    if (this.environment.windowHeightPx > 0) this.foldWindowHeightVp = ui.px2vp(this.environment.windowHeightPx);
    this.environment.syncPosture(this.foldWindowWidthVp || this.phoneWidth, this.foldWindowHeightVp || this.phoneHeight, Number(this.breakpoint));
    const posture = this.foldLayout.posture;
    hilog.info(0x0000, 'PersonaLayout', 'widthBreakpoint=%{public}s posture=%{public}s wide=%{public}s',
      String(this.breakpoint), posture, String(this.wide()));
    if (this.lastFoldPosture && posture !== this.lastFoldPosture) {
      const wasTriFold = this.lastFoldPosture === 'trifold_unfolded' || this.lastFoldPosture === 'trifold_angled';
      const isNowTriFold = posture === 'trifold_unfolded' || posture === 'trifold_angled';
      if (wasTriFold !== isNowTriFold) {
        this.foldInspectorUserToggled = false;
        this.foldInspectorOpen = this.foldLayout.autoShowInspector;
      }
    }
    this.lastFoldPosture = posture;
    if (!this.foldLayout.supportsInspector) {
      this.foldInspectorUserToggled = false;
      this.foldInspectorOpen = false;
    }
    if (this.showFoldInspector() && this.foldLayout.collapseRailForInspector && !this.collapsed && !this.foldInspectorUserToggled) {
      this.collapsed = true;
      this.foldInspectorCollapsedRail = true;
    } else if (!this.foldLayout.collapseRailForInspector && this.foldInspectorCollapsedRail) {
      this.collapsed = false;
      this.foldInspectorCollapsedRail = false;
    }
    if (!this.wide() && (this.collapsed || this.foldInspectorCollapsedRail)) {
      this.collapsed = false;
      this.foldInspectorCollapsedRail = false;
    }
    if (oldWide !== this.wide() && this.wide() && this.model.section === 1 && this.stack.size() === 0 && this.model.sessions.length)
      this.openRow(this.model.sessions.find((row: ResourceRow) => row.key() === this.model.selected.key()) || this.model.sessions[0]);
  }
  private bindDeviceOwner(model: ClientModel): void {
    const host = this.getUIContext().getHostContext();
    if (host) deviceFeatures.bind(model, host as common.UIAbilityContext,
      (title: string, message: string, approve: () => void, deny: () => void): void => {
        this.getUIContext().showAlertDialog({ title: title, message: message,
          primaryButton: { value: tr('cancel'), action: deny }, secondaryButton: { value: tr('confirm'), action: approve } });
      });
  }
  private restoreDeviceOwner(owner: ClientModel): void { if (deviceFeatures.boundTo(owner)) this.bindDeviceOwner(this.model); }
  aboutToAppear(): void {
    this.syncConversationRows(); this.getUIContext().setKeyboardAvoidMode(KeyboardAvoidMode.RESIZE); this.updateBreakpoint(); this.consumePairingLink(); this.restoreScope(); this.consumeNotification();
    this.bindDeviceOwner(this.model);
    this.applications.bind(this.model); this.applications.refresh();
  }
  aboutToDisappear(): void { this.clearPairTransport(); phonePasswordLogin.cancel(); if (this.assistantStatusTimer !== -1) clearInterval(this.assistantStatusTimer); this.agentWorkspace.hide(); this.applications.dispose(); this.dismissChatActions(); }
  @Monitor('model.profile', 'model.connected', 'model.features') applicationOwnerChanged(): void {
    this.applications.bind(this.model); this.applications.connectionChanged();
  }
  @Monitor('model.profile') scopeChanged(): void { if (this.activeScope !== this.model.scope()) { this.tokenUsageOpen = false; this.quickPaletteOpen = false; this.standingAuthSheetShown = false; this.fullSearchSheetShown = false; this.autoChatsSheetShown = false; this.chatForward.end(); this.chatForward.clearNew(); this.pendingSearchHit = ''; this.pendingSearchTarget = ''; this.chatSearchShown = false; this.drawerSearching = false; this.phoneSettingsMachine = undefined; this.phoneSettingsQuery = ''; this.phoneSettingsTrail = []; this.phoneSettingsShown = false; this.phoneSettingsDepth = -1; this.pendingInboxHandoff = ''; this.projectCatalog.clear(); this.agentWorktree = undefined; this.pickedAgentTree = ''; this.agentWorkspace.hide(); this.spacesShown = false; this.dismissChatActions(); this.showSheet = false; this.agentFormRevision++; this.creatingAgent = false; this.agentTodo = undefined; this.readerShown = false; this.readerAddress = ''; this.readerTitle = ''; this.detailModel.hideConversation(); this.detailModel = this.model; this.chatReady = false; this.detailRoute = ''; this.routeRevision++; this.bindDeviceOwner(this.model); } this.syncConversationRows(); this.restoreScope(); deviceFeatures.context(); }
  @Monitor('model.conversations', 'model.mainId', 'model.mainMachineId', 'model.sidebarConfig', 'model.connected', 'spacesShown', 'breakpoint', 'collapsed', 'sidebarTaskHistory.revision') conversationsChanged(): void { this.syncConversationRows(); }
  @Monitor('model.appearance') appearanceChanged(): void {
    this.getUIContext().getHostContext()?.getApplicationContext().setColorMode(this.model.appearance === 1 ? ConfigurationConstant.ColorMode.COLOR_MODE_LIGHT : this.model.appearance === 2 ? ConfigurationConstant.ColorMode.COLOR_MODE_DARK : ConfigurationConstant.ColorMode.COLOR_MODE_NOT_SET);
    if (this.model.appearance === 1) this.environment.dark = false;
    else if (this.model.appearance === 2) this.environment.dark = true;
  }
  @Monitor('detailModel.connected') chatMenuConnectionChanged(): void { if (!this.detailModel.connected && (this.chatTitleMenuVisible || this.chatBodyMenuVisible)) this.dismissChatActions(); }
  @Monitor('model.directorySessions', 'model.directoryMachines') projectDirectoryChanged(): void {
    if (this.model.section === 4 || this.model.section === 1 && this.agentWorkspace.groupingMode === 'repo') this.projectCatalog.syncRows(this.model);
  }
  @Monitor('model.connected') connectionChanged(): void { if (this.model.section === 4 || this.model.section === 1 && this.agentWorkspace.groupingMode === 'repo') this.projectCatalog.refresh(this.model); deviceFeatures.context(); if (this.model.connected && this.shownName && this.activeChild === this.childKey(this.shownName, this.shownParam)) this.restoreTarget(this.shownName, this.shownParam); }
  @Monitor('model.forgottenEpoch') forgetNavigation(): void {
    for (const key of this.paths.keys()) if (isHubStateKey('view:' + key, this.model.forgottenProfile)) this.paths.delete(key);
    this.rowVisibility.clear(); this.visibilityEpoch++;
  }
  private async restoreScope(): Promise<void> {
    const scope = this.model.scope(); if (scope === this.activeScope) return;
    this.activeScope = scope; this.stack = this.paths.get(scope + ':' + this.model.section) || new NavPathStack(); this.navigationRestoredScope = ''; this.rowVisibility.clear(); const epoch = ++this.restoring, intent = this.navigationIntent; this.syncPhonePath();
    let view: Record<string, Object>;
    try { view = Wire.parse(await this.model.loadView('navigation')); }
    catch (_) { if (epoch === this.restoring && scope === this.model.scope()) { this.activeScope = ''; this.model.error = 'cache_unavailable'; } return; }
    if (epoch !== this.restoring || scope !== this.model.scope()) return;
    if (intent === this.navigationIntent) this.model.section = Math.max(0, Math.min(11, Wire.number(view, 'section')));
    Wire.rows(view, 'tabs').forEach((tab: Record<string, Object>) => {
      const key = scope + ':' + Wire.number(tab, 'index');
      if (!this.paths.has(key)) { const stack = new NavPathStack();
        Wire.rows(tab, 'routes').forEach((route: Record<string, Object>, index: number) => {
          const name = Wire.text(route, 'name'); let param = Wire.text(route, 'param');
          if (index === 0 && ['session', 'conversation'].includes(name)) { const row = this.routeRow(param), data = row.data(); data['navigation_root'] = true; row.raw = JSON.stringify(data); param = JSON.stringify(row); }
          stack.pushPath({ name: name, param: param }, false);
        });
        this.paths.set(key, stack);
      }
    });
    this.stack = this.paths.get(scope + ':' + this.model.section) || new NavPathStack();
    this.paths.set(scope + ':' + this.model.section, this.stack); this.syncPhonePath();
    this.navigationRestoredScope = scope;
    if (this.meetingServices.pendingOpen && this.model.ready) { this.consumeMeetingReminder(); return; }
    if (this.model.projectsRequested && this.model.ready) { this.consumeProjectLaunch(); return; }
    if (this.model.pendingXiaoYiRoute && this.model.ready) { this.consumeXiaoYiRoute(); return; }
    if (intent !== this.navigationIntent) this.savePath();
    if (this.model.section === 2 && !this.model.inboxPageVisible() || this.model.section === 5 && !this.model.todosPageVisible() || this.model.section === 6 && !this.model.knowledgePageVisible()) {
      this.openSpace('persona');
      return;
    }
    if (this.model.section === 2 && !this.stack.size()) this.openResources('inbox');
    else if (this.model.section === 3 && !this.stack.size()) this.openSettingsRoot();
    else if (this.model.section === 4) this.projectCatalog.refresh(this.model);
    else if (this.model.section >= 5 && !this.stack.size()) this.showPlaceRoot(this.model.section);
    else this.ensureMainDestination();
  }
  @Monitor('model.sidebarConfig', 'model.demo') hiddenPagesChanged(): void {
    const viewingInbox = this.model.section === 2 || this.phoneTop === 'inbox' || this.phoneTop === 'resources' && this.phoneParam === 'inbox';
    const viewingTodos = this.model.section === 5 || this.phoneTop === 'todo' || this.phoneTop === 'resources' && this.phoneParam === 'todo';
    const viewingKnowledge = this.model.section === 6 || this.phoneTop === 'knowledge' || this.phoneTop === 'knowledge_meetings' || this.phoneTop === 'resources' && this.phoneParam === 'knowledge';
    if (viewingInbox && !this.model.inboxPageVisible() || viewingTodos && !this.model.todosPageVisible() || viewingKnowledge && !this.model.knowledgePageVisible()) {
      this.openSpace('persona');
    }
  }
  @Monitor('model.pairingLink') pairingChanged(): void { this.consumePairingLink(); }
  @Monitor('model.notificationTarget') notificationChanged(): void { this.consumeNotification(); }
  @Monitor('model.pendingXiaoYiRoute', 'model.ready') xiaoYiRouteChanged(): void { this.consumeXiaoYiRoute(); }
  private consumeXiaoYiRoute(): void {
    if (!this.model.pendingXiaoYiRoute || !this.model.ready) return;
    const raw = this.model.pendingXiaoYiRoute;
    const parsed = Wire.parse(raw);
    const dest = Wire.text(parsed, 'dest');
    if (!dest) { this.model.pendingXiaoYiRoute = ''; return; }
    if (dest !== 'settings_xiaoyi' && (!this.model.profile.id || this.navigationRestoredScope !== this.model.scope())) return;
    this.model.pendingXiaoYiRoute = '';
    this.spacesShown = false;
    this.phoneSettingsShown = false;
    if (dest === 'assistant') {
      this.openSpace('persona');
      return;
    }
    if (dest === 'agents') {
      this.openSpace('agents');
      return;
    }
    if (dest === 'session') {
      const machine = Wire.text(parsed, 'machine'), session = Wire.text(parsed, 'session');
      const profile = Wire.text(parsed, 'profile') || this.model.profile.id;
      const manager = Wire.text(parsed, 'manager') || this.model.profile.manager;
      if (profile && machine && session) {
        this.model.openNotification(Wire.payload(['profile', 'manager', 'machine', 'session'], [profile, manager, machine, session])).catch(() => {});
      }
      return;
    }
    if (dest === 'todos') { this.openSpace(this.model.todosPageVisible() ? 'todo' : 'persona'); return; }
    if (dest === 'knowledge') { this.openSpace(this.model.knowledgePageVisible() ? 'knowledge' : 'persona'); return; }
    if (dest === 'health') { this.openSpace('health'); return; }
    if (dest === 'inbox') { this.openSpace(this.model.inboxPageVisible() ? 'inbox' : 'persona'); return; }
    if (dest === 'recording') {
      const autoStart = Wire.bool(parsed, 'autoStart');
      if (!this.model.isRecordingModuleAllowed()) {
        // Fail closed on recording starts when the Hub's recording module is off (#15985 / #15978);
        // stored recordings stay readable and an active recording stays stoppable.
        this.getUIContext().getPromptAction().showToast({ message: tr('xiaoyi_recording_module_off') });
        this.openSpace('recordings');
        return;
      }
      if (autoStart) {
        this.openSpace('recordings');
        return;
      }
      this.openSpace('recordings');
      return;
    }
    if (dest === 'dictate') { this.openSpace('persona'); this.newConversation(); return; }
    if (dest === 'settings_xiaoyi') {
      if (!this.wide() || !this.model.profile.id) {
        this.phoneSettingsQuery = '';
        this.phoneSettingsTrail = pushPhoneSetting([], 'phone_xiaoyi');
        if (this.model.profile.id && !this.wide()) this.spacesShown = true;
        this.phoneSettingsShown = true;
      } else {
        this.openSettings('phone_xiaoyi');
      }
      return;
    }
    if (dest === 'share') {
      const sharedText = formatSharedDraftText(Wire.text(parsed, 'title'), Wire.text(parsed, 'text'), Wire.text(parsed, 'url'));
      this.openSpace('persona');
      if (sharedText) {
        const owner = this.model, scope = owner.scope(), generation = owner.connectionGeneration();
        this.openMain().then(() => {
          if (this.model === owner && owner.owns(generation, scope)) {
            const current = owner.draft.trim();
            const next = !current ? sharedText : current.includes(sharedText) ? owner.draft : current + '\n\n' + sharedText;
            owner.updateDraft(next);
          }
        }).catch(() => {});
      }
    }
  }
  private consumeNotification(): void {
    if (!this.model.notificationTarget) return;
    const row = this.routeRow(this.model.notificationTarget); this.model.notificationTarget = '';
    if (!this.model.sessions.concat(this.model.conversations).some((item: ResourceRow) => item.key() === row.key())) return;
    this.spacesShown = false; this.phoneSettingsShown = false; this.chatSearchShown = false;
    this.changeSection(row.kind === 'conversation' ? 0 : 1); this.openRow(row, false);
  }
  private consumePairingLink(): void {
    const link = this.model.pairingLink;
    if (!link) return;
    this.model.pairingLink = '';
    this.openSheet('connect', 'manual', link);
  }
  private savePath(): void {
    if (this.activeScope !== this.model.scope()) this.restoreScope();
    this.syncPhonePath(); this.navigationIntent++;
    const child = this.projectCatalog.createdSelection;
    if (child && !this.phonePath.some((name:string,i:number) => name === 'project_tree' && this.stack.getParamByIndex(i) === child.key)) {
      this.projectCatalog.createdSelection = undefined;
    }
    const key = this.model.scope() + ':' + this.model.section;
    if (this.navigationRestoredScope === this.model.scope() || this.stack.size() || this.paths.has(key)) this.paths.set(key, this.stack);
    const tabs: Record<string, Object>[] = [];
    for (let index = 0; index < 12; index++) { const stack = this.paths.get(this.model.scope() + ':' + index);
      const routes: Record<string, Object>[] = [];
      stack?.getAllPathName().forEach((name: string, position: number) => routes.push(Wire.payload(['name', 'param'], [name, stack.getParamByIndex(position) as string || ''])));
      tabs.push(Wire.payload(['index', 'routes'], [index, routes]));
    }
    if (this.navigationRestoredScope !== this.model.scope()) return;
    this.model.saveView('navigation', JSON.stringify(Wire.payload(['section', 'tabs'], [this.model.section, tabs]))).catch(() => this.model.error = 'failed');
  }
  private changeSection(index: number): void {
    this.pendingInboxHandoff = ''; this.savePath(); this.routeRevision++; this.activeChild = ''; this.shownName = ''; this.chatReady = false; this.detailRoute = ''; this.detailModel.hideConversation(); this.model.releaseTerminal(); this.model.section = index; this.stack = this.paths.get(this.model.scope() + ':' + index) || new NavPathStack(); this.query = ''; this.filter = 0;
    if (index === 1 && this.wide() && !this.stack.size() && this.model.sessions.length) this.openRow(this.model.sessions[0]);
    if (index === 2) this.model.listResources('inbox'); this.savePath();
  }
  private routeTarget(row: ResourceRow, root: boolean): ResourceRow {
    const copy = NavigationContext.rowFrom(JSON.stringify(row));
    if (copy.kind === 'session' || copy.kind === 'conversation') { const data = copy.data(); data['navigation_root'] = root; copy.raw = JSON.stringify(data); }
    return copy;
  }
  private openRow(row: ResourceRow, root: boolean = true): void {
    const name = NavigationContext.destination(row);
    this.chatReady = false; this.detailRoute = '';
    if (row.kind === 'session') { this.stack.clear(); this.tool = 0; }
    this.stack.pushPath({ name: name, param: JSON.stringify(this.routeTarget(row, root || !this.stack.size())) }); this.savePath();
  }
  private async openLinkedResource(row: ResourceRow, beforePush?: (destination:string) => boolean): Promise<void> {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), source = this.activeChild;
    try {
      if (!['inbox', 'todo', 'knowledge', 'file', 'todo_source', 'todo_thread'].includes(row.kind)) await this.model.viewFor(row);
      if (!this.model.owns(generation, scope) || this.activeChild !== source) return;
      const target = this.routeTarget(row, false), name = NavigationContext.destination(target), data = target.data();
      if (beforePush && !beforePush(name)) return;
      this.chatReady = false; this.detailRoute = '';
      const handoff = target.kind === 'inbox' && Wire.bool(data, 'open_handoff');
      if (Wire.has(data, 'open_handoff')) data['open_handoff'] = false; target.raw = JSON.stringify(data);
      const param = JSON.stringify(target);
      if (handoff) this.pendingInboxHandoff = this.childKey(name, param);
      this.stack.pushPath({ name: name, param: param }); this.savePath();
    } catch (error) { if (this.model.owns(generation, scope) && this.activeChild === source) this.model.reportError(error as Error); }
  }
  private openFrom(name: string, param: Object, row: ResourceRow): void {
    if (this.activeChild === this.childKey(name, param)) { if (!row.group) row.group = NavigationContext.from(name, param as string).row.group || this.model.profile.id; this.openLinkedResource(row); }
  }
  private createAgentFrom(name: string, param: Object, row: ResourceRow): void {
    if (this.activeChild === this.childKey(name, param)) this.prepareAgent(row);
  }
  private openComputer(row: ResourceRow): void {
    if (row.kind !== 'machine' || !row.id) return;
    const param = JSON.stringify(row), names = this.stack.getAllPathName(), top = names.length - 1;
    if (top >= 0 && names[top] === 'computer' && this.stack.getParamByIndex(top) === param) return;
    this.stack.pushPath({ name: 'computer', param: param }); this.savePath();
  }
  private selectedComputer(): ResourceRow | undefined {
    return this.detailModel.machines.find((row: ResourceRow) => row.id === this.detailModel.selected.machine);
  }
  private async handoffResource(row: ResourceRow): Promise<void> {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), source = this.activeChild;
    try {
      const target = await this.model.handoff(row);
      if (this.model.owns(generation, scope) && this.activeChild === source) await this.openLinkedResource(target);
    } catch (error) { if (this.model.owns(generation, scope) && this.activeChild === source) this.model.reportError(error as Error); }
  }
  private childKey(name: string, param: Object): string { return name + ':' + (param as string); }
  private pushChat(row: ResourceRow): void {
    const names = this.stack.getAllPathName();
    const index = names.findIndex((name: string, position: number) => name === 'conversation' && this.routeRow(this.stack.getParamByIndex(position) as string).key() === row.key());
    if (index >= 0) this.stack.popToIndex(index);
    else this.stack.pushPath({ name: 'conversation', param: JSON.stringify(this.routeTarget(row, !this.stack.size())) });
    this.savePath();
  }
  private askingInboxSources: string = '';
  private async askFromInboxSources(text:string):Promise<void> {
    const owner=this.model,scope=owner.scope(),generation=owner.connectionGeneration();
    if (!text.trim() || Array.from(text).length>8000 || this.askingInboxSources===scope || !owner.connected || !owner.features.includes('inbox_sources')) return;
    this.askingInboxSources=scope;
    const active=():boolean=>this.model===owner && owner.connected && owner.owns(generation,scope) && owner.features.includes('inbox_sources');
    try {
      const target=owner.mainActivityRow() || await owner.openMain();
      if (!active() || !target.machine || !target.id || target.group && target.group!==owner.profile.id || target.manager && target.manager!==owner.profile.manager) return;
      const path='/api/v1/sessions/'+encodeURIComponent(target.machine)+'/'+encodeURIComponent(target.session || target.id)+'/messages';
      if (owner.operationBlocked(path)) { owner.error='pending_action'; return; }
      await owner.repo().send(target,text,util.generateRandomUUID(),'',[],active);
      if (active()) {
        this.phoneSettingsShown=false; this.spacesShown=false;
        if (owner.section!==0) this.changeSection(0);
        this.stack.clear(); this.defaultMainAttempt=''; await this.openMain();
      }
    } catch (failure) { if (active()) owner.reportError(failure as Error); }
    finally { if (this.askingInboxSources===scope) this.askingInboxSources=''; }
  }
  private async openMain(): Promise<void> {
    this.navigationIntent++; if (this.model.section !== 0) this.changeSection(0);
    const owner = this.model, scope = owner.scope(), generation = owner.connectionGeneration(), intent = this.navigationIntent, revision = this.routeRevision, stack = this.stack;
    const current = (): boolean => this.model === owner && owner.owns(generation, scope) && owner.section === 0 && intent === this.navigationIntent && revision === this.routeRevision && stack === this.stack;
    while (this.defaultMainPending && current()) await new Promise<void>((resolve) => this.mainReadWaiters.push(resolve));
    if (current()) await this.resolveMainDestination();
  }
  private newConversation(): void {
    this.navigationIntent++;
    const scope = this.model.scope(), generation = this.model.connectionGeneration(), ownerKey = JSON.stringify([scope, generation]);
    this.creatingConversations.set(ownerKey, (this.creatingConversations.get(ownerKey) || 0) + 1);
    this.model.newConversation().then((row: ResourceRow) => { if (this.model.owns(generation, scope)) this.pushChat(row); })
      .catch((error: Error) => { if (this.model.owns(generation, scope)) this.model.reportError(error); })
      .finally(() => { const remaining = (this.creatingConversations.get(ownerKey) || 1) - 1; if (remaining) this.creatingConversations.set(ownerKey, remaining); else this.creatingConversations.delete(ownerKey); });
  }
  private openResources(kind: string): void { this.resourceKind = kind; this.model.listResources(kind); this.stack.pushPath({ name: 'resources', param: kind }); this.savePath(); }
  private openSettings(page: string, contextual: boolean = false): void { if (page === 'standing_authorizations') { this.spacesShown = false; this.phoneSettingsShown = false; this.standingAuthSheetShown = true; return; } if (page === 'appearance' || page === 'phone_agent_layout' || page === 'phone_look_layout') this.ensureAgentWorkspace(); this.settingsPage = page; this.stack.pushPath({ name: 'settings', param: NavigationContext.settings(page, contextual ? this.detailModel.selected : undefined) }); this.savePath(); }
  private settingsFamily(page: string): string { return ['extension_detail', 'extension_catalog'].includes(page) ? 'extensions' : page === 'connector_detail' ? 'connectors' : page === 'channel_detail' ? 'channels' : page; }
  private isAdminPage(page: string): boolean { return ['assistant', 'memory', 'managers', 'phone_new_assistant', 'launchers', 'engine', 'hub_update', 'node_update'].includes(page); }
  private settingsLabel(page: string): string {
    if (page === 'appearance') return 'phone_settings_display';
    if (page === 'phone_meeting_options') return 'meetings';
    return page === 'session_effort' ? 'reasoning_effort' : page === 'session_model' ? 'model' : page === 'session_permissions' ? 'permission_mode' : page === 'phone_new_assistant' ? 'phone_new_assistant' : page === 'phone_agent_options' ? 'agent' : page === 'phone_language_options' ? 'settings_language_options_title' : page === 'ssh' ? 'ssh_settings' : page === 'assistant' ? 'admin_identity' : page === 'memory' ? 'admin_memory' : page === 'managers' ? 'admin_managers' : page === 'launchers' ? 'admin_launchers' : page === 'engine' ? 'admin_machine_engine' : page === 'hub_update' ? 'admin_hub_update' : page === 'node_update' ? 'admin_node_update' : page; }
  private adminMenus(name: string, param: Object): NavigationMenuItem[] { return [{ value: tr('refresh'), symbolIcon: new SymbolGlyphModifier($r('sys.symbol.arrow_clockwise')), action: () => { if (this.activeChild === this.childKey(name, param)) this.adminRefreshRevision++; } }]; }
  private settingsTitle(param: Object): string { const descriptor = NavigationContext.from('settings', param as string); return descriptor.scoped && ['extension_detail', 'connector_detail', 'channel_detail', 'extension_catalog','model_purpose'].includes(descriptor.page) ? descriptor.row.title : tr(this.settingsLabel(descriptor.page)); }
  private openSettingsDetail(page: string, row: ResourceRow): void { this.stack.pushPath({ name: 'settings', param: NavigationContext.settings(page, row) }); this.savePath(); }
  private openApplications(): void {
    const param = JSON.stringify([this.model.profile.id, this.model.profile.manager]);
    const found = this.stack.getAllPathName().findIndex((name: string): boolean => name === 'applications');
    if (found >= 0) this.stack.popToIndex(found);
    else this.stack.pushPath({ name: 'applications', param: param });
    this.savePath(); this.applications.refresh();
  }
  private openApplication(entry: ExtensionNavigationEntry): void {
    if (!this.applications.accepts(entry)) return;
    const found = this.stack.getAllPathName().findIndex((name: string): boolean => name === 'applications');
    if (found < 0) return;
    this.stack.popToIndex(found);
    this.openExtension(this.model, entry.extensionId, entry.uiId, entry.title, '', {}, entry.extensionId, entry.uiId);
  }
  private applicationAllowed(param: Object): boolean {
    const row = this.routeRow(param), data = row.data(), root = Wire.text(data, 'navigation_extension');
    return !root || this.applications.allows(row.group, row.manager, root, Wire.text(data, 'navigation_ui'));
  }
  private openExtension(owner: ClientModel, extensionId: string, uiId: string, title: string, state: string = '', context: Record<string, Object> = Wire.payload([], []), navigationExtension: string = '', navigationUI: string = ''): void {
    if (!extensionId || !uiId || !owner.profile.id) return;
    if (this.stack.getAllPathName().filter((name: string) => name === 'extension').length >= 8) { this.getUIContext().getPromptAction().showToast({ message: tr('extension_ui_stack_limit') }); return; }
    const row = new ResourceRow(extensionId, title || extensionId, 'extension'); row.group = owner.profile.id; row.manager = owner.profile.manager;
    row.raw = JSON.stringify(Wire.payload(['ui', 'state', 'context', 'instance', 'navigation_extension', 'navigation_ui'], [uiId, state, context, util.generateRandomUUID(), navigationExtension, navigationUI]));
    this.stack.pushPath({ name: 'extension', param: JSON.stringify(row) }); this.savePath();
  }
  private extensionNavigate(name: string, param: Object, effect: ExtensionUINavigationEffect): void {
    if (this.activeChild !== this.childKey(name, param) || !this.applicationAllowed(param) || effect.kind !== 'push') return;
    const source = this.routeRow(param).data();
    this.openExtension(this.detailModel, effect.extensionId, effect.uiId, effect.title, effect.state, effect.context, Wire.text(source, 'navigation_extension'), Wire.text(source, 'navigation_ui'));
  }
  private extensionMenus(name: string, param: Object): NavigationMenuItem[] { return [{ value: tr('refresh'), symbolIcon: new SymbolGlyphModifier($r('sys.symbol.arrow_clockwise')), action: () => { if (this.activeChild === this.childKey(name, param)) this.extensionRefreshRevision++; } }]; }
  private settingsKind(param: Object): string { return NavigationContext.from('settings', param as string).page; }
  private targetReady(name: string, param: Object): boolean {
    if (!this.chatReady || this.detailRoute !== this.childKey(name, param)) return false;
    if (name !== 'session' && name !== 'conversation') return true;
    return this.detailOwner === this.detailModel &&
      this.detailSelection === JSON.stringify([this.detailModel.scope(), this.detailModel.selected.key()]);
  }
  private markDetailReady(key: string): void {
    this.detailOwner = this.detailModel;
    this.detailSelection = JSON.stringify([this.detailModel.scope(), this.detailModel.selected.key()]);
    this.detailRoute = key; this.chatReady = true;
  }
  private settingsModel(param: Object): ClientModel { return NavigationContext.from('settings', param as string).scoped ? this.detailModel : this.model; }
  private clearPairTransport(): void {
    this.confirmingScan = false;
    this.pairSsh.clear(); this.editingPairSsh = false;
    this.pairAccessID = ''; this.pairAccessSecret = ''; this.pairAccessExpanded = false;
  }

  onPageHide(): void {
    if (this.showSheet && this.sheetKind === 'connect') {
      this.pairSsh.cancelAttempt();
      if (!this.scanning && !this.passwordAuthorizing) {
        this.pairingRevision++; this.clearPairTransport();
      }
    }
  }

  private reportPairFailure(error: Error): void {
    const cause = error as Object as Record<string, Object>, raw = cause['code'];
    const name = typeof raw === 'string' && raw ? raw : error.message;
    this.model.diagnosticCode = name && /^[a-z_0-9]{1,64}$/.test(name) ? name :
      typeof raw === 'number' ? String(raw) : 'pairing_failed';
    hilog.error(0x0000, 'PersonaPair', '%{public}s', this.model.diagnosticCode);
    if (['cloudflare_access_required','cloudflare_access_rejected','cloudflare_access_incomplete','cloudflare_access_invalid'].includes(name)) {
      this.pairAccessExpanded = true;
      this.model.error = name;
    } else {
      this.model.error = name && name.startsWith('ssh_') && tr(name) !== name ? name : 'connection_failed';
    }
  }

  private async passwordConnect(): Promise<void> {
    const host = this.getUIContext().getHostContext() as common.UIAbilityContext;
    if (!host || this.model.busy || this.passwordAuthorizing) return;
    const address = this.address, revision = this.pairingRevision;
    const generation = this.model.connectionGeneration(), scope = this.model.scope();
    const active = (): boolean => this.showSheet && this.sheetKind === 'connect' && this.pairByPassword &&
      this.address === address && revision === this.pairingRevision && this.model.owns(generation, scope);
    this.passwordAuthorizing = true; this.model.error = '';
    try {
      const endpoint = await phonePasswordLogin.authorize(address, tr('phone'),
        (page: string): Promise<void> => host.startAbility({ action: 'ohos.want.action.viewData', uri: page }), active);
      if (!active()) return;
      await this.model.useEndpoint(endpoint, endpoint.baseUrl, active);
      if (this.model.profile.id === endpoint.hubId && revision === this.pairingRevision) {
        this.showSheet = false; this.address = ''; this.pairCode = '';
      }
    } catch (err) {
      const message = (err as Error).message;
      if (active() && message !== 'request_updated') {
        this.model.error = ['phone_password_address', 'phone_password_expired', 'phone_password_browser', 'pairing_response_invalid'].includes(message) ? message : 'connection_failed';
      }
    } finally { if (revision === this.pairingRevision) this.passwordAuthorizing = false; }
  }

  private confirmScannedPairing(value: string): void {
    if (!this.showSheet || !this.compactPairing() || this.pairByPassword || this.pairManually || this.confirmingScan || this.model.busy) return;
    const pairing = Pairing.parse(value);
    const revision = ++this.scanRevision, sheet = this.pairingRevision;
    const generation = this.model.connectionGeneration(), scope = this.model.scope();
    const active = (): boolean => this.showSheet && this.compactPairing() && !this.pairByPassword && !this.pairManually &&
      this.confirmingScan && this.scanRevision === revision && this.pairingRevision === sheet && this.model.owns(generation, scope);
    const cancel = (): void => { if (active()) this.confirmingScan = false; };
    this.confirmingScan = true;
    this.getUIContext().showAlertDialog({ title: tr('phone_pair_title'), message: pairing.initialAddress,
      cancel: cancel, primaryButton: { value: tr('cancel'), action: cancel },
      secondaryButton: { value: tr('connect'), action: () => {
        if (!active()) return;
        this.confirmingScan = false;
        this.address = value; this.pairCode = ''; this.model.error = '';
        this.connect();
      } } });
  }

  private async choosePairingPhoto(): Promise<void> {
    if (this.scanning || this.model.busy) return;
    const revision = ++this.scanRevision, pairing = this.pairingRevision;
    const generation = this.model.connectionGeneration(), scope = this.model.scope();
    const active = (): boolean => revision === this.scanRevision && pairing === this.pairingRevision &&
      this.showSheet && this.sheetKind === 'connect' && !this.pairByPassword && this.model.owns(generation, scope);
    this.scanning = true; this.model.error = '';
    try {
      const options = new photoAccessHelper.PhotoSelectOptions();
      options.MIMEType = photoAccessHelper.PhotoViewMIMETypes.IMAGE_TYPE;
      options.maxSelectNumber = 1;
      const selected = await new photoAccessHelper.PhotoViewPicker().select(options);
      if (!active() || !selected.photoUris.length) return;
      const found = await detectBarcode.decode({ uri: selected.photoUris[0] }, { scanTypes: [scanCore.ScanType.QR_CODE] });
      if (!active()) return;
      const codes = Array.from(new Set<string>(found.map((item: scanBarcode.ScanResult) => item.originalValue)));
      if (codes.length !== 1) throw new Error('phone_pair_photo_invalid');
      this.scanning = false;
      this.confirmScannedPairing(codes[0]);
    } catch (err) {
      if (active() && (err as BusinessError).code !== 13900012) this.model.error = 'phone_pair_photo_invalid';
    } finally { if (revision === this.scanRevision) this.scanning = false; }
  }

  private async connect(): Promise<void> {
    const generation = this.model.connectionGeneration(), scope = this.model.scope(), address = this.address, code = this.pairCode, revision = this.pairingRevision;
    const accessID = this.pairAccessID, accessSecret = this.pairAccessSecret;
    const current = (): boolean => this.pairAccessID === accessID && this.pairAccessSecret === accessSecret && this.showSheet && this.sheetKind === 'connect' && revision === this.pairingRevision && this.model.owns(generation, scope) && this.address === address && this.pairCode === code;
    this.model.busy = true; this.model.error = '';
    try {
      const input = isPairingAddress(address.trim()) ? Pairing.parse(address.trim()) : Pairing.manual(address.trim(), code.trim());
      input.setCloudflareAccess(accessID, accessSecret);
      const pairing = this.pairSsh.prepare(input, current);
      const target = await pairing.inspect();
      if (!current()) return;
      this.model.busy = false;
      this.getUIContext().showAlertDialog({ title: tr('verify'), message: target.name + '\n' + target.baseUrl + '\n' + target.hubId,
        cancel: () => { if (current()) this.pairSsh.cancelAttempt(); },
        primaryButton: { value: tr('cancel'), action: () => { if (current()) this.pairSsh.cancelAttempt(); } },
        secondaryButton: { value: tr('trust'), action: () => { if (current()) this.redeem(pairing, target, current); } } });
    } catch (error) {
      if (!current()) return;
      this.pairSsh.cancelAttempt(); this.reportPairFailure(error as Error);
    } finally { if (this.model.owns(generation, scope)) this.model.busy = false; }
  }
  private async redeem(pairing: Pairing, target: PairingTarget, admit: () => boolean): Promise<void> {
    const revision = this.pairingRevision, address = this.address, code = this.pairCode;
    try {
      await this.model.pair(pairing, target, admit);
      if (this.sheetKind === 'connect' && revision === this.pairingRevision && this.address === address && this.pairCode === code && this.model.profile.id === target.hubId) { this.pairCode = ''; this.address = ''; this.showSheet = false; }
    } catch (error) { if (admit()) { this.pairSsh.cancelAttempt(); this.reportPairFailure(error as Error); } }
  }
  private menus(): NavigationMenuItem[] {
    if (this.rail()) return [];
    return [
      { value: tr(this.peer() === 1 ? 'new_agent' : 'new_chat'), symbolIcon: new SymbolGlyphModifier(Symbols.plus), action: () => this.peer() === 1 ? this.openSheet('newagent') : this.newConversation() },
      { value: tr('conversation_spaces'), symbolIcon: new SymbolGlyphModifier(Symbols.spaces), action: () => this.spacesShown = true }
    ];
  }
  @Builder shellMenu() {
    Row({ space: this.foldLayout.supportsInspector ? Tokens.spaceSm : Tokens.spaceS }) {
      if (!this.rail() && this.model.profile.id) {
        this.quickPaletteButton(this.foldLayout.supportsInspector ? 32 : 36, true)
      }
      if (!this.rail()) ForEach(this.menus(), (item: NavigationMenuItem, index: number) => {
        Button({ type: ButtonType.Circle }) { SymbolGlyph(index === 0 ? Symbols.plus : Symbols.spaces).fontSize(Tokens.symbolFont).fontColor([Colors.ink]) }
          .width(this.foldLayout.supportsInspector ? 32 : 36).height(this.foldLayout.supportsInspector ? 32 : 36).padding(0)
          .backgroundColor(Colors.styleID === 'glass' ? Colors.elevatedGlass : Colors.surface)
          .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: Colors.styleEdge || Colors.hairlineGlass, radius: (this.foldLayout.supportsInspector ? 32 : 36) / 2 })
          .shadow(PhoneStyle.floatingElevation(this.environment.dark))
          .responseRegion(ButtonStyles.touchRegion(this.foldLayout.supportsInspector ? 32 : 36))
          .accessibilityText(item.value as string).onClick(() => { Haptics.tap(); item.action?.(); }).id(index === 0 ? 'shell.new' : 'shell.spaces')
      })
      if (!this.rail() && this.wide() && this.foldLayout.supportsInspector) {
        Button({ type: ButtonType.Circle }) {
          SymbolGlyph($r('sys.symbol.info_circle')).fontSize(Tokens.symbolFont).fontColor([this.showFoldInspector() ? Colors.link : Colors.ink])
        }.width(32).height(32).padding(0)
          .responseRegion(ButtonStyles.touchRegion(32))
          .backgroundColor(this.showFoldInspector() ? Colors.selection : (Colors.styleID === 'glass' ? Colors.elevatedGlass : Colors.surface))
          .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: this.showFoldInspector() ? Colors.link : (Colors.styleEdge || Colors.hairlineGlass), radius: 16 })
          .shadow(PhoneStyle.floatingElevation(this.environment.dark))
          .accessibilityText(tr('fold_inspector_title'))
          .onClick(() => this.toggleFoldInspector())
          .id('shell.fold.inspector')
      }
    }.height(this.headerHeight()).alignItems(VerticalAlign.Center).padding({ right: this.foldLayout.supportsInspector ? 10 : Tokens.page })
  }
  @Builder navigationMenus(name: string, param: Object) {
    if (this.wide()) this.destinationMenus(name, param)
  }
  private compactChatMenu(name: string): boolean {
    return !this.wide() && (name === 'session' || name === 'tools' || name === 'conversation');
  }
  @Computed get phoneChatActionsReady(): boolean {
    return this.compactChatMenu(this.phoneTop) && this.activeChild === this.childKey(this.phoneTop,this.phoneParam) &&
      this.targetReady(this.phoneTop,this.phoneParam);
  }
  @Builder compactSessionMore() { this.sessionMoreButton(false) }
  @Builder sessionMoreButton(slim:boolean) {
    Button({type:ButtonType.Circle}) {
      Row({space:3}) {
        ForEach([0,1,2], (_dot:number) => {
          Circle().width(3.5).height(3.5).fill(Colors.ink)
        }, (dot:number) => String(dot))
      }
    }.width(slim ? 36 : this.sessionActionSize()).height(slim ? 36 : this.sessionActionSize()).padding(0)
      .backgroundColor(Colors.styleID === 'glass' ? Colors.elevatedGlass : Colors.surface)
      .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
      .responseRegion(ButtonStyles.touchRegion(slim ? 36 : this.sessionActionSize()))
      .border({width:PhoneStyle.surfaceEdgeWidth(0.5),color:Colors.styleEdge || Colors.hairlineGlass,radius:(slim ? 36 : this.sessionActionSize())/2})
      .shadow(PhoneStyle.floatingElevation(this.environment.dark))
      .accessibilityText(tr('more')).id('chat.title.more')
      .enabled(this.phoneChatActionsReady).onClick(() => this.showChatActions(this.phoneTop,this.phoneParam))
      .bindMenu(this.chatTitleMenuVisible && this.phoneChatActionsReady,this.chatActionsMenu,this.chatMenuOptions(this.chatMenuRevision))
  }
  @Builder destinationMenus(name: string, param: Object) {
    if (!this.mainDestination(name, param)) Row({ space: Tokens.spaceS }) {
      if (name==='knowledge' && this.routeRow(param).path==='recordings') {
        Button({type:ButtonType.Circle}) {
          SymbolGlyph($r('sys.symbol.label')).fontSize(Tokens.symbolFont).fontColor([Colors.link])
        }.width(Tokens.chrome).height(Tokens.chrome).padding(0)
          .backgroundColor(Colors.styleID === 'glass' ? Colors.elevatedGlass : Colors.surface)
          .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: Colors.styleEdge || Colors.hairlineGlass, radius: Tokens.chrome / 2 })
          .shadow(PhoneStyle.floatingElevation(this.environment.dark))
          .responseRegion(ButtonStyles.touchRegion(Tokens.chrome))
          .accessibilityText(tr('tags')).enabled(this.targetReady(name,param)).onClick(() => {
            if (this.activeChild===this.childKey(name,param) && this.targetReady(name,param)) this.recordingEditRevision++;
          }).id('destination.action.0')
      } else ForEach(this.destinationMenuItems(name, param), (item: NavigationMenuItem, index: number) => {
        Button({ type: ButtonType.Circle }) {
          if (item.symbolIcon) SymbolGlyph().attributeModifier(item.symbolIcon).fontSize(Tokens.symbolFont)
          else if (name === 'session' || name === 'conversation' || name === 'tools') {
            Row({ space: 3 }) {
              ForEach([0, 1, 2], (_dot: number) => {
                Circle().width(3.5).height(3.5).fill(Colors.ink)
              }, (dot: number) => String(dot))
            }
          } else Image(item.icon).width(Tokens.symbol).height(Tokens.symbol).fillColor(Colors.ink)
        }.width(Tokens.chrome).height(Tokens.chrome).padding(0)
          .backgroundColor(Colors.styleID === 'glass' ? Colors.elevatedGlass : Colors.surface)
          .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: Colors.styleEdge || Colors.hairlineGlass, radius: Tokens.chrome / 2 })
          .shadow(PhoneStyle.floatingElevation(this.environment.dark))
          .responseRegion(ButtonStyles.touchRegion(Tokens.chrome))
          .accessibilityText(item.value as string).enabled(item.isEnabled !== false).onClick(() => item.action?.())
          .id(name === 'session' || name === 'conversation' || name === 'tools' ? 'chat.title.more' : 'destination.action.' + String(index))
          .bindMenu((name === 'session' || name === 'conversation' || name === 'tools') && this.chatTitleMenuVisible && this.activeChild === this.childKey(name, param),
            this.chatActionsMenu, this.chatMenuOptions(this.chatMenuRevision))
      })
      if (this.rail() && this.foldLayout.supportsInspector && (name === 'session' || name === 'conversation' || name === 'tools')) {
        Button({ type: ButtonType.Circle }) {
          SymbolGlyph($r('sys.symbol.info_circle')).fontSize(Tokens.symbolFont).fontColor([this.showFoldInspector() ? Colors.link : Colors.ink])
        }.width(Tokens.chrome).height(Tokens.chrome).padding(0)
          .backgroundColor(this.showFoldInspector() ? Colors.selection : (Colors.styleID === 'glass' ? Colors.elevatedGlass : Colors.surface))
          .backgroundBlurStyle(Colors.styleID === 'glass' ? BlurStyle.COMPONENT_THICK : BlurStyle.NONE)
          .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: this.showFoldInspector() ? Colors.link : (Colors.styleEdge || Colors.hairlineGlass), radius: Tokens.chrome / 2 })
          .shadow(PhoneStyle.floatingElevation(this.environment.dark))
          .responseRegion(ButtonStyles.touchRegion(Tokens.chrome))
          .accessibilityText(tr('fold_inspector_title'))
          .onClick(() => this.toggleFoldInspector())
          .id('fold.inspector.toggle')
      }
    }.height(this.headerHeight()).alignItems(VerticalAlign.Center).padding({ right: this.wide() ? Tokens.page : 0 })
  }
  private openSettingsRoot(): void { this.stack.pushPath({ name: 'settings_root', param: '' }); this.savePath(); }
  private openSpace(kind: string): void {
    this.phoneSettingsDepth = -1;
    if (kind === 'inbox' && !this.model.inboxPageVisible() || kind === 'todo' && !this.model.todosPageVisible() || kind === 'knowledge' && !this.model.knowledgePageVisible()) {
      kind = 'persona';
    }
    if (kind === 'settings' && !this.wide() && this.spacesShown) {
      this.phoneSettingsMachine = undefined;
      this.phoneSettingsQuery = '';
      this.phoneSettingsTrail = [];
      this.phoneSettingsShown = true; return;
    }
    this.spacesShown = false;
    if (kind === 'projects') this.openProjects();
    else if (kind === 'agents') {
      if (this.model.section === 1) { this.stack.clear(); this.savePath(); }
      this.browsePeer(1);
    }
    else if (kind === 'persona') { this.selectPeer(0); this.stack.clear(); this.openMain(); }
    else if (kind === 'health' && !this.wide()) {
      if (this.phoneTop !== 'resources' || this.phoneParam !== 'health') this.openResources('health');
    }
    else if (kind === 'hub') this.openSheet('hub');
    else if (kind === 'token_usage') { this.tokenUsageOpen = true; }
    else if (kind === 'collapse') this.collapsed = true;
    else {
      const index = ['persona', 'agents', 'inbox', 'settings', 'projects', 'todo', 'knowledge', 'health', 'devices', 'schedules', 'recordings', 'applications'].indexOf(kind);
      if (index < 2) return;
      const before = this.model.section;
      if (before === 0 || before === 1 || before === 4) this.previousPrimary = before;
      if (before !== index) this.changeSection(index);
      else { this.stack.clear(); this.savePath(); }
      if (!this.stack.size()) this.showPlaceRoot(index);
    }
  }
  private showPlaceRoot(index: number): void {
    if (index === 2) this.openResources('inbox');
    else if (index === 3) this.openSettingsRoot();
    else if (index === 5) this.openResources('todo');
    else if (index === 6) this.openResources('knowledge');
    else if (index === 7) this.openResources('health');
    else if (index === 8) this.openSettings('machines');
    else if (index === 9) this.openSettings('schedules');
    else if (index === 10) this.pushPlace('recordings', '');
    else if (index === 11) this.openApplications();
  }
  private spaceSymbol(kind: string): Resource {
    if (kind === 'persona') return $r('sys.symbol.house');
    if (kind === 'schedules') return $r('sys.symbol.alarm');
    if (kind === 'recordings') return Symbols.agents;
    if (kind === 'projects') return Symbols.folder;
    if (kind === 'agents') return Symbols.agents;
    if (kind === 'inbox') return $r('sys.symbol.inbox');
    if (kind === 'todo') return $r('sys.symbol.list_checkmark');
    if (kind === 'knowledge') return $r('sys.symbol.books_vertical_fill');
    if (kind === 'health') return $r('sys.symbol.heart');
    if (kind === 'devices') return $r('sys.symbol.matebook_and_phone_fill');
    if (kind === 'token_usage') return Symbols.health;
    if (kind === 'settings') return Symbols.gear;
    if (kind === 'applications') return Symbols.apps;
    if (kind === 'collapse') return Symbols.collapse;
    return Symbols.spaces;
  }
  private spaceColor(kind: string): ResourceColor {
    if (kind === 'inbox' || kind === 'persona') return Colors.link;
    if (kind === 'todo') return Colors.positive;
    if (kind === 'knowledge') return Colors.ai;
    if (kind === 'health') return Colors.teal;
    if (kind === 'devices') return Colors.working;
    return Colors.secondary;
  }
  private spaceTitle(kind: string): string {
    if (kind === 'hub') return this.model.profile.name || tr('hub');
    if (kind === 'persona') return tr('main_chat');
    if (kind === 'todo') return tr('todos');
    if (kind === 'health') return tr('phone_drawer_health');
    if (kind === 'schedules') return tr('phone_drawer_schedules');
    if (kind === 'devices') return tr('phone_machines');
    if (kind === 'token_usage') return tr('token_usage_title');
    if (kind === 'recordings') return tr('knowledge_kind_meetings');
    return tr(kind);
  }
  private matchesDrawer(text: string): boolean { return !this.drawerSearch.trim() || text.toLowerCase().includes(this.drawerSearch.trim().toLowerCase()); }
  @Computed get drawerManagers(): ResourceRow[] {
    return this.model.listedManagers();
  }
  @Computed get drawerManager(): ResourceRow | undefined {
    return this.drawerManagers.find((row: ResourceRow) => row.id === this.model.profile.manager && !row.isInstallingManager());
  }
  @Computed get drawerHasMultipleInstalled(): boolean {
    return this.drawerManagers.filter((row: ResourceRow) => !row.isInstallingManager()).length > 1;
  }
  @Monitor('assistantMenuScope') assistantStatusClock(): void {
    if (this.assistantStatusTimer !== -1) clearInterval(this.assistantStatusTimer);
    this.assistantStatusTimer = -1;
    if (this.assistantMenuScope) {
      this.assistantMenuNow = Date.now();
      this.assistantStatusTimer = setInterval(() => { this.assistantMenuNow = Date.now(); },15000);
    }
  }
  private managerLine(row: ResourceRow): string {
    const data = row.data(), now = this.assistantMenuScope ? this.assistantMenuNow : Date.now();
    if (row.isInstallingManager()) {
      const expiry = Date.parse(Wire.text(data,'installing'));
      if (!Number.isFinite(expiry)) return tr('admin_manager_installing');
      return expiry <= now ? tr('phone_switcher_expired') : tr('phone_switcher_expiry').replace('{minutes}',String(Math.max(1,Math.round((expiry-now)/60000))));
    }
    if (!Wire.has(data,'online')) return '';
    if (Wire.bool(data,'online')) {
      const host = Wire.text(data,'host');
      return host ? tr('admin_manager_online') + ' · ' + host : tr('admin_manager_online');
    }
    const seen = Date.parse(Wire.text(data,'last_seen'));
    if (!Number.isFinite(seen)) return tr('offline');
    const locale = this.model.language === 'default' ? i18n.System.getSystemLanguage() : this.model.language;
    const seconds = (seen-now)/1000, elapsed = Math.abs(seconds);
    const units: string[] = ['second','minute','hour','day','week','month','year'];
    const sizes: number[] = [1,60,3600,86400,7*86400,30.44*86400,365.25*86400];
    const next = sizes.findIndex((duration: number) => elapsed < duration);
    const index = next < 0 ? sizes.length-1 : Math.max(0,next-1);
    const relative = new intl.RelativeTimeFormat(locale,{numeric:'auto',style:'long'}).format(Math.round(seconds/sizes[index]),units[index]);
    return tr('phone_switcher_synced').replace('{time}',relative);
  }
  private assistantMenuWidth(): number {
    const available = this.phoneWidth || this.getUIContext().px2vp(this.environment.windowWidthPx);
    return Math.min(PhoneStyle.sidebarMetric(300, this.environment.fontScale), Math.max(1, available - 20));
  }
  private assistantMenuTint(): ResourceColor {
    return this.look.accentId === 'style' && usesSystemStyleColors(this.model.style) ? $r('app.color.persona_needs_mark') : Colors.link;
  }
  private assistantWell(): number { return PhoneStyle.sidebarMetric(36, this.environment.fontScale); }
  private assistantAvatarColor(row: ResourceRow): ResourceColor {
    if (row.id === this.model.rosterDefaultId || !this.model.rosterDefaultId) return $r('app.color.persona_switcher_avatar');
    const hues: ResourceColor[] = this.environment.dark ? ['#5B9BFF','#4CC38A','#FF9F0A','#FF6B8B','#5E5CE6'] :
      ['#2E6FD0','#1D7A4C','#B35400','#D62E5C','#5856D6'];
    const hue = new util.TextEncoder().encodeInto(row.id).reduce((sum: number, byte: number) => sum + byte, 0);
    return hues[hue % hues.length];
  }
  private assistantMenuAction(scope: string, destination: string, row?: ResourceRow): void {
    if (!scope || this.assistantMenuScope !== scope || this.model.scope() !== scope || !this.drawerAcceptsTap()) return;
    this.assistantMenuScope = '';
    if (row) {
      const target = this.drawerManagers.find((item: ResourceRow) => item.id === row.id);
      if (!target) return;
      if (!target.isInstallingManager()) { this.model.selectListedManager(target.id,scope); return; }
      this.spacesShown = false;
      this.settingsPage = 'managers';
      this.stack.pushPath({name:'settings',param:NavigationContext.installingManager(scope,target.id)});
      this.savePath(); return;
    }
    this.spacesShown = false;
    this.openSettings(destination);
  }
  @Builder managerChoice(row: ResourceRow, scope: string) {
    Button({type:ButtonType.Normal}) {
      Row({space:12}) {
        Stack({alignContent:Alignment.BottomEnd}) {
          Text(Array.from(row.title || row.id)[0]).fontSize(Tokens.interfaceFontVp(17,this.environment.fontScale)).fontWeight(600)
            .maxLines(1).fontColor($r('app.color.persona_switcher_surface')).textAlign(TextAlign.Center).width(this.assistantWell()).height(this.assistantWell())
            .backgroundColor(this.assistantAvatarColor(row)).borderRadius(this.assistantWell()/2)
          if (row.isInstallingManager() || Wire.has(row.data(),'online')) Circle().width(10).height(10)
            .fill(row.isInstallingManager() ? $r('app.color.persona_dock_warning') : Wire.bool(row.data(),'online') ? $r('app.color.persona_switcher_online') : this.environment.dark ? '#636366' : '#8E8E93').stroke($r('app.color.persona_switcher_surface')).strokeWidth(2)
        }
        Column({space:2}) {
          Row({space:4}) {
            Text(row.title || row.id).fontSize(Tokens.interfaceFontVp(17,this.environment.fontScale)).fontWeight(600).fontColor($r('app.color.persona_switcher_ink'))
              .lineHeight(String(PhoneStyle.formSize(20.33,27.67,this.environment.fontScale))+'vp').maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).flexShrink(1)
            if (row.id === this.model.rosterDefaultId && this.drawerHasMultipleInstalled && !row.isInstallingManager())
              Text(tr('admin_default')).fontSize(Tokens.interfaceFontVp(11,this.environment.fontScale)).fontColor(this.assistantMenuTint())
                .maxLines(1).padding({left:4,right:4}).borderRadius(4).backgroundColor(Colors.control)
          }
          if (this.managerLine(row)) Text(this.managerLine(row)).fontSize(Tokens.interfaceFontVp(12,this.environment.fontScale)).fontColor(Colors.secondary)
            .lineHeight(String(PhoneStyle.formSize(14.33,21.67,this.environment.fontScale))+'vp').maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        }.alignItems(HorizontalAlign.Start).layoutWeight(1)
        if (row.id === this.model.profile.manager) SymbolGlyph(Symbols.check).fontSize(Tokens.interfaceFontVp(17,this.environment.fontScale))
          .fontColor([this.assistantMenuTint()]).id('spaces.assistant_check')
      }.width('100%').padding({left:8,right:8,top:4,bottom:4}).constraintSize({minHeight:PhoneStyle.sidebarMetric(44,this.environment.fontScale)})
    }.width('100%').padding(0).backgroundColor(Color.Transparent)
      .opacity(!row.isInstallingManager() && Wire.has(row.data(),'online') && !Wire.bool(row.data(),'online') ? 0.55 : 1)
      .accessibilityText([row.title || row.id,this.managerLine(row)].filter((text:string) => !!text).join(' · '))
      .accessibilitySelected(row.id === this.model.profile.manager)
      .onClick(() => this.assistantMenuAction(scope,'',row)).id('spaces.assistant.' + row.id)
  }
  @Builder assistantMenuCommand(create: boolean, scope: string) {
    Button({type:ButtonType.Normal}) {
      Row({space:12}) {
        Stack() {
          Circle().width(this.assistantWell()).height(this.assistantWell()).fill(create ? this.assistantMenuTint() : $r('app.color.persona_switcher_ink')).opacity(0.15)
          SymbolGlyph(create ? Symbols.plus : Symbols.gear).fontSize(Tokens.interfaceFontVp(create ? 17 : 19,this.environment.fontScale)).fontWeight(600)
            .fontColor([create ? this.assistantMenuTint() : $r('app.color.persona_switcher_ink')])
        }
        Text(tr(create ? 'phone_new_assistant' : 'phone_manage_assistants') + (create ? '' : '…')).fontSize(Tokens.interfaceFontVp(17,this.environment.fontScale))
          .fontWeight(create ? 600 : FontWeight.Normal).fontColor(create ? this.assistantMenuTint() : $r('app.color.persona_switcher_ink'))
          .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).layoutWeight(1)
      }.width('100%').padding({left:8,right:8}).height(PhoneStyle.sidebarMetric(44,this.environment.fontScale))
    }.width('100%').padding(0).backgroundColor(Color.Transparent)
      .onClick(() => this.assistantMenuAction(scope,create ? 'phone_new_assistant' : 'managers'))
      .id(create ? 'spaces.new_assistant' : 'spaces.manage_assistants')
  }
  @Builder drawerManagerMenu(scope: string) {
    Column() {
      if (this.drawerManagers.length) {
        Scroll() {
          Column() {
            ForEach(this.drawerManagers,(row:ResourceRow) => { this.managerChoice(row,scope); },(row:ResourceRow) => row.key())
          }.width('100%')
        }.width('100%').constraintSize({maxHeight:Math.max(this.assistantWell(),this.getUIContext().px2vp(this.environment.windowHeightPx)*0.52)})
          .scrollBar(BarState.Auto).id('spaces.assistant_list')
        Divider().color(Colors.divider).strokeWidth(0.33).margin({left:8,right:8,top:4,bottom:4})
      }
      this.assistantMenuCommand(true,scope)
      this.assistantMenuCommand(false,scope)
    }.width(this.assistantMenuWidth()).padding(8).backgroundColor($r('app.color.persona_switcher_surface')).id('spaces.assistant_panel')
  }
  private assistantMenuOptions(scope: string, epoch: number): CustomPopupOptions {
    return { builder: (): void => this.drawerManagerMenu(scope), placement: Placement.Bottom, width: this.assistantMenuWidth(),
      offset:{x:3,y:PhoneStyle.formSize(0,-1/3,this.environment.fontScale)}, enableArrow:true, arrowHeight:13, arrowWidth:26, targetSpace:0, radius:16, popupColor:$r('app.color.persona_switcher_surface'),
      backgroundBlurStyle:BlurStyle.NONE, mask:true, autoCancel:true, focusable:true,
      onStateChange:(state:PopupStateChangeParam):void => { if (!state.isVisible && epoch === this.assistantMenuEpoch) this.assistantMenuScope = ''; } };
  }
  @Builder assistantHeading(row: ResourceRow) {
    Row() {
      Button({ type: ButtonType.Normal }) {
        Row({ space: 8 }) {
          Stack({ alignContent: Alignment.BottomEnd }) {
            Image(this.look.avatarUri || $r('app.media.phone_persona_face')).width(38).height(38).objectFit(ImageFit.Cover).borderRadius(19)
            if (Wire.has(row.data(), 'online')) Circle().width(10).height(10)
              .fill(Wire.bool(row.data(), 'online') ? Colors.positive : Colors.secondary)
              .stroke(Colors.surface).strokeWidth(2)
          }
          Column({ space: 1 }) {
            Row({ space: 4 }) {
              Text(row.title || row.id).fontSize(18).maxFontScale(Tokens.navigationTextMaxScale).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
                .maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis }).flexShrink(1)
              SymbolGlyph(Symbols.down).fontSize('12vp').fontColor([Colors.secondary])
            }.width('100%')
            if (this.managerLine(row)) Text(this.managerLine(row)).fontSize(13).maxFontScale(Tokens.navigationTextMaxScale).fontColor(Colors.secondary)
              .maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis }).width('100%')
          }.alignItems(HorizontalAlign.Start).layoutWeight(1)
        }.width('100%').constraintSize({ minHeight: this.drawerMetric(44) })
      }.width('100%').padding(0).backgroundColor(Color.Transparent)
        .accessibilityText([tr('phone_switch_assistant'), row.title || row.id, this.managerLine(row)].filter((s: string) => !!s).join(' · '))
        .onClick(() => { if (this.drawerAcceptsTap()) { Haptics.tap(); this.assistantMenuEpoch++; this.assistantMenuScope = this.model.scope(); } })
        .bindPopup(!!this.assistantMenuScope && this.assistantMenuScope === this.model.scope(),this.assistantMenuOptions(this.assistantMenuScope,this.assistantMenuEpoch)).id('spaces.assistant_switcher')
    }.layoutWeight(1).clip(true).justifyContent(FlexAlign.Start)
  }
  @Computed get drawerAgentRows():ResourceRow[] {
    if (!this.mergedPhone()) return [];
    return this.model.directorySessions.filter((r:ResourceRow) => !Wire.bool(r.data(),'assistant') && !Wire.bool(r.data(),'archived') && !Wire.bool(r.data(),'is_subagent'))
      .sort((a:ResourceRow,b:ResourceRow) => (Date.parse(Wire.text(b.data(),'updated_at')) || 0) - (Date.parse(Wire.text(a.data(),'updated_at')) || 0));
  }
  @Computed get shellHasInboxAction(): boolean {
    if (!this.model.inboxPageVisible()) return false;
    return this.model.inbox.some((row:ResourceRow) => !row.isDone() && inboxTier(row.data()) === 'action');
  }
  private placeCount(kind:string): number {
    if (kind === 'inbox') return this.model.inbox.filter((row:ResourceRow) => !row.isDone() &&
      inboxTier(row.data()) === 'action' && !Wire.bool(row.data(),'read')).length;
    return this.model.phoneTodoBadge();
  }
  private drawerMetric(points: number): number {
    return this.wide() ? points : PhoneStyle.sidebarMetric(points,this.environment.fontScale);
  }
  private drawerPlaceSelected(kind: string): boolean {
    if (kind === 'persona' && !this.wide()) {
      return this.peer() === 0 && !!this.model.mainId && this.mainDestination(this.phoneTop, this.phoneParam);
    }
    return this.activeModule() === kind;
  }
  @Builder drawerDivider() {
    ListItem() {
      Divider().color(Colors.divider).strokeWidth(Tokens.divider)
        .margin({left:12,right:12,top:this.wide() ? 4 : 8,bottom:this.wide() ? 6 : 8})
    }
  }
  @Builder spaceRow(kind: string) {
    if (this.matchesDrawer(this.spaceTitle(kind))) ListItem() {
      Row({ space: 16 }) {
        SymbolGlyph(this.spaceSymbol(kind)).fontSize(String(this.drawerMetric(this.wide() ? 20 : 22))+'vp').fontColor([Colors.ink]).width(this.drawerMetric(24))
        Text(this.spaceTitle(kind)).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.ink).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).layoutWeight(1)
        if ((kind === 'inbox' || kind === 'todo') && this.placeCount(kind) > 0) {
          Text(this.placeCount(kind)>99 ? '99+' : String(this.placeCount(kind))).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Medium)
            .fontColor(Colors.link).padding({left:6,right:6,top:2,bottom:2}).backgroundColor(Colors.control).borderRadius(16)
            .accessibilityText(String(this.placeCount(kind))).id('spaces.count.' + kind)
        }
      }.padding({left:12,right:12,top:4,bottom:4}).constraintSize({minHeight:this.drawerMetric(44)}).width('100%')
    }.borderRadius(10).backgroundColor(this.drawerPlaceSelected(kind) ? this.wide() ? Colors.control : Colors.phoneDrawerSelection : Color.Transparent)
      .selectable(this.wide()).selected(this.wide() && this.drawerPlaceSelected(kind))
      .accessibilitySelected(this.drawerPlaceSelected(kind))
      .onClick(() => { if (this.drawerAcceptsTap()) this.openSpace(kind); }).id('spaces.' + kind)
  }
  @Builder spacesContent() {
    Column() {
      if (!this.wide() && !this.drawerSearching) {
        Row({ space: 8 }) {
          if (this.drawerManager) this.assistantHeading(this.drawerManager)
          else Text(tr('persona')).fontSize(27).maxFontScale(Tokens.interfaceScaleFor(27)).fontWeight(FontWeight.Bold).fontColor(Colors.ink).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).layoutWeight(1)
          PhoneIconButton({symbol: $r('sys.symbol.magnifyingglass'), label:tr('search'), identity:'spaces.search_toggle', controlDiameter:this.drawerMetric(46),
            action:() => { if (this.drawerAcceptsTap()) { this.drawerSearch = ''; this.drawerSearching = true; } }})
        }.width('100%').padding({left:24,right:24,top:8,bottom:8})
      }
      if (!this.wide() && this.drawerSearching) {
        PhoneConversationSearch({model:this.model,inline:true,showScopes:this.mergedPhone() || this.model.section === 1 || this.model.section === 4,
          onCancel:() => this.drawerSearching = false,onOpen:(row:ResourceRow,hit:string) => this.openPickedChat(row,hit)}).layoutWeight(1)
      } else {
      List() {
        this.spaceRow('persona')
        this.drawerDivider()
        if (this.wide() || this.mergedPhone()) {
          this.spaceRow('agents')
          this.spaceRow('projects')
        }
        this.spaceRow('recordings')
        if (this.model.inboxPageVisible()) this.spaceRow('inbox')
        if (this.model.todosPageVisible()) this.spaceRow('todo')
        this.spaceRow('schedules')
        if (this.model.knowledgePageVisible()) this.spaceRow('knowledge')
        this.spaceRow('health')
        this.spaceRow('token_usage')
        if (this.applications.visible) this.spaceRow('applications')
        this.spaceRow('devices')
        if (!this.wide()) this.drawerDivider()
        if (this.wide()) this.spaceRow('settings')
        if (this.directionRowBindings.length) ListItem() {
          Text(tr('phone_sidebar_directions')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Medium)
            .fontColor(Colors.secondary).width('100%').padding({left:13,top:12,bottom:4}).id('spaces.directions')
        }
        ForEach(this.directionRowBindings, (binding: BoundRow<ResourceRow>) => {
          this.drawerChat(binding,'spaces.direction.')
        }, (binding: BoundRow<ResourceRow>) => binding.key)
        if (this.conversationRowBindings.length) ListItem() {
          Row() {
            Text(tr('phone_drawer_recent')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontWeight(FontWeight.Medium).fontColor(Colors.secondary).layoutWeight(1)
            SymbolGlyph(this.recentExpanded ? Symbols.down : Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
          }.width('100%').constraintSize({minHeight:this.wide() ? 44 : PhoneStyle.formSize(35.7,43,this.environment.fontScale)}).padding({left:12,right:12,top:this.wide() ? 6 : 16,bottom:4}).onClick(() => { if (this.drawerAcceptsTap()) this.recentExpanded = !this.recentExpanded; }).id('spaces.recent')
        }
        if (this.recentExpanded || this.drawerSearch) ForEach(this.conversationRowBindings, (binding: BoundRow<ResourceRow>) => {
          this.drawerChat(binding,'spaces.recent.')
        }, (binding: BoundRow<ResourceRow>) => binding.key)
        ListItem() {
          Row() { Text(tr('flow_all_chats')).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.ink).layoutWeight(1); SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary]) }
            .width('100%').constraintSize({minHeight:this.drawerMetric(44)}).padding({left:12,right:12})
        }.onClick(() => { if (this.drawerAcceptsTap()) { this.spacesShown = false; this.autoChatsSheetShown = true; } }).id('spaces.all_chats')
        if (this.model.connected) {
          ListItem() {
            Row({ space: 10 }) {
              SymbolGlyph(Symbols.search).fontSize('16vp').fontColor([Colors.link])
              Text(tr('assistant_full_search_title')).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.ink).layoutWeight(1)
              SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
            }.width('100%').constraintSize({ minHeight: this.drawerMetric(44) }).padding({ left: 12, right: 12 })
          }.onClick(() => { if (this.drawerAcceptsTap()) { this.spacesShown = false; this.fullSearchSheetShown = true; } }).id('spaces.full_search')
          ListItem() {
            Row({ space: 10 }) {
              SymbolGlyph($r('sys.symbol.checkmark_shield')).fontSize('16vp').fontColor([Colors.ai])
              Text(tr('standing_auth_title')).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.ink).layoutWeight(1)
              SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
            }.width('100%').constraintSize({ minHeight: this.drawerMetric(44) }).padding({ left: 12, right: 12 })
          }.onClick(() => { if (this.drawerAcceptsTap()) { this.spacesShown = false; this.standingAuthSheetShown = true; } }).id('spaces.standing_auth')
        }
        if (starredStore.isOffered(this.model)) {
          ListItem() {
            Row({ space: 10 }) {
              SymbolGlyph($r('sys.symbol.star')).fontSize('16vp').fontColor([Colors.working])
              Text(tr('starred_messages')).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.ink).layoutWeight(1)
              SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
            }.width('100%').constraintSize({ minHeight: this.drawerMetric(44) }).padding({ left: 12, right: 12 })
          }.onClick(() => { if (this.drawerAcceptsTap()) { this.spacesShown = false; this.starredSheetShown = true; } }).id('spaces.starred')
        }
        if (this.model.connected) {
          ListItem() {
            Row({ space: 10 }) {
              SymbolGlyph($r('sys.symbol.archivebox')).fontSize('16vp').fontColor([Colors.secondary])
              Text(tr('archived_chats')).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontColor(Colors.ink).layoutWeight(1)
              if (this.model.conversations.filter((row: ResourceRow) => Wire.bool(row.data(), 'archived')).length > 0) {
                Text(String(this.model.conversations.filter((row: ResourceRow) => Wire.bool(row.data(), 'archived')).length))
                  .fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
              }
              SymbolGlyph(Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
            }.width('100%').constraintSize({ minHeight: this.drawerMetric(44) }).padding({ left: 12, right: 12 })
          }.onClick(() => { if (this.drawerAcceptsTap()) { this.spacesShown = false; this.archivedSheetShown = true; } }).id('spaces.archived')
        }
        if (this.mergedPhone() && this.drawerAgentRows.length) {
          ListItem() {
            Row() {
              Text(tr('agents')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontWeight(FontWeight.Bold).fontColor(Colors.secondary).layoutWeight(1)
              Text(String(this.drawerAgentRows.length)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
              SymbolGlyph(this.drawerAgentsExpanded ? Symbols.down : Symbols.expand).fontSize('12vp').fontColor([Colors.secondary])
            }.width('100%').constraintSize({minHeight:44}).padding({left:13,right:14}).onClick(() => { if (this.drawerAcceptsTap()) this.drawerAgentsExpanded = !this.drawerAgentsExpanded; }).id('spaces.agent_group')
          }
          if (this.drawerAgentsExpanded) ForEach(this.drawerAgentRows.slice(0,12),(r:ResourceRow) => {
            ListItem() {
              Row({space:8}) {
                SymbolGlyph(Symbols.agent).fontSize('16vp').fontColor([Colors.secondary])
                Column({space:2}) {
                  Text(r.title || tr('agents')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.ink).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
                  Text(Wire.text(r.data(),'last_text') || r.path).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
                }.layoutWeight(1).alignItems(HorizontalAlign.Start)
              }.padding({left:15,right:12,top:5,bottom:5}).width('100%').constraintSize({minHeight:42})
            }.onClick(() => { if (this.drawerAcceptsTap()) this.openPickedChat(r); }).accessibilityGroup(true).id('spaces.agent')
          },(r:ResourceRow) => JSON.stringify([r.key(),r.title,r.state,Wire.text(r.data(),'last_text')]))
        }
        if (this.wide() || this.mergedPhone()) this.spaceRow('hub')
        if (this.wide() && !this.rail()) this.spaceRow('collapse')
      }.width('100%').layoutWeight(1).padding({left:this.wide() ? 5 : 8,right:this.wide() ? 5 : 8})
      }
      if (!this.wide()) {
        Row({space:8}) {
          Button({type:ButtonType.Capsule}) {
            Row({space:6}) {
              SymbolGlyph(Symbols.edit).fontSize('16vp').fontColor([Colors.onAction])
              Text(tr('phone_new_chat_title')).fontSize(15).maxFontScale(Tokens.navigationTextMaxScale).fontWeight(FontWeight.Medium).fontColor(Colors.onAction)
            }
          }.height(this.drawerMetric(46)).padding({left:24,right:24}).backgroundColor(Colors.action).accessibilityText(tr('phone_new_chat_title'))
            .onClick(() => { if (!this.drawerAcceptsTap()) return; this.spacesShown = false; if (this.model.section !== 0) this.changeSection(0); this.newConversation(); }).id('spaces.new')
          Blank().layoutWeight(1)
          PhoneIconButton({symbol:Symbols.gear,label:tr('settings'),identity:'spaces.settings',controlDiameter:this.drawerMetric(46),action:() => { if (this.drawerAcceptsTap()) this.openSpace('settings'); }})
            .bindSheet($$this.phoneSettingsShown, this.phoneSettings(), {height: this.compactFormHeight(),showClose:false,dragBar:false,backgroundColor:Colors.sheetBackground,
              onWillDismiss:(dismiss:DismissSheetAction) => {
                if (dismiss.reason === DismissReason.PRESS_BACK && this.phoneSettingsPage) this.backPhoneSetting();
                else dismiss.dismiss();
              }})
        }.width('100%').padding({left:24,right:24,top:12,bottom:6})
      }
    }.width('100%').height('100%').padding({bottom:this.spacesInset + 12}).backgroundColor(this.wide() ? Colors.surface : Colors.phoneDrawerSurface)
      .id('shell.spaces.content').onAreaChange(() => this.measureSpaces())
  }
  private measureSpaces(): void {
    if (!this.spacesLayoutReady) return;
    const value = nativeSheetInset(this.getUIContext(), 'shell.spaces.content', this.environment.safeBottomPx);
    if (Math.abs(value - this.spacesInset) > 0.1) this.spacesInset = value;
  }
  @Monitor('environment.safeBottomPx') safeBottomChanged(): void { this.measureSpaces(); }
  @Monitor('showSheet', 'spacesShown') modalChanged(): void {
    if (!this.showSheet && this.sheetKind === 'connect') {
      this.pairingRevision++; this.scanRevision++; phonePasswordLogin.cancel();
      this.scanning = false; this.passwordAuthorizing = false; this.clearPairTransport();
    }
    if (!this.showSheet && !this.spacesShown) this.ensureMainDestination();
  }
  private openHistory(): void { this.stack.pushPath({ name: 'history', param: JSON.stringify(this.detailModel.selected) }); this.savePath(); }
  private openTools(path: string = '', tab: number = 0): void { const data = Wire.record(JSON.parse(JSON.stringify(this.detailModel.selected)) as Object); if (path) data['open_file'] = path; if (tab) data['entry_tab'] = tab; this.stack.pushPath({ name: 'tools', param: JSON.stringify(data) }); this.savePath(); }
  private destinationMenuItems(name: string, param: Object): NavigationMenuItem[] {
    if (name === 'knowledge' && this.routeRow(param).path === 'recordings') {
      return [{ value: tr('tags'), symbolIcon: new SymbolGlyphModifier($r('sys.symbol.label')), isEnabled: this.targetReady(name, param),
        action: () => { if (this.activeChild === this.childKey(name, param)) this.recordingEditRevision++; } }];
    }
    if (name === 'settings' && this.isAdminPage(this.settingsKind(param))) return this.adminMenus(name, param);
    if (name === 'extension' && this.applicationAllowed(param)) return this.extensionMenus(name, param);
    if (this.mainDestination(name, param)) return [];
    if (name === 'session' || name === 'conversation' || name === 'tools' && !this.wide()) return [{ value: tr('more'), icon: $r('sys.media.ohos_ic_public_more'), isEnabled: this.targetReady(name, param), action: () => this.showChatActions(name, param) }];
    return [];
  }
  private chatMenuOptions(revision: number): MenuOptions {
    return { onDisappear: () => this.closeChatActions(revision) };
  }
  @Builder chatActionsMenu() {
    Menu() {
      ForEach(this.chatMenuActions, (item: ChatActionEntry) => {
        MenuItem({ content: item.title, startIcon: item.icon }).constraintSize({ minHeight: Tokens.touch })
          .onClick(() => item.action()).id(item.identity || '')
      }, (_item: ChatActionEntry, index: number) => String(this.chatMenuRevision) + ':' + String(index))
    }
  }
  private closeChatActions(revision: number): void {
    if (revision !== this.chatMenuRevision) return;
    this.chatTitleMenuVisible = false; this.chatBodyMenuVisible = false;
  }
  private dismissChatActions(): void {
    this.chatMenuRevision++; this.chatTitleMenuVisible = false; this.chatBodyMenuVisible = false; this.chatMenuActions = [];
  }
  private async triggerDetailAutoRename(owner: ClientModel): Promise<void> {
    const target = owner.selected;
    const label = target.title || tr('agent_untitled');
    const err = await owner.autoRenameAgent(target);
    if (err && this.detailModel === owner) {
      Haptics.error();
      toastCenter.error(trError(err) + ' · ' + label);
    }
  }
  private showChatActions(name: string, param: Object): void {
    const owner = this.detailModel, key = owner.selected.key(), generation = owner.connectionGeneration(), scope = owner.scope();
    const valid = (): boolean => this.activeChild === this.childKey(name, param) && this.targetReady(name, param) &&
      this.detailModel === owner && owner.owns(generation, scope) && owner.selected.key() === key;
    if (!valid()) { this.dismissChatActions(); return; }
    const sheets: ChatActionEntry[] = [];
    if (owner.connected && owner.selected.kind==='conversation' && owner.repo().isDefaultManager() && this.directionPermissionRoutes.hasFiling() &&
      this.directionPermissionRoutes.directions.some((direction:ChatDirection)=>direction.id===(owner.selected.session || owner.selected.id))) {
      sheets.push({title:tr('phone_default_permissions'),identity:'direction-default-permissions',
        action:()=>{ if (valid()) { this.directionPermissionsTarget=JSON.stringify([owner.scope(),owner.selected.key()]);
          this.directionPermissionsVisible=true; } }});
    }
    if (!this.wide() && owner.selected.machine && (name === 'session' || name === 'tools')) {
      if (name === 'tools') sheets.push({title: tr('session_back_to_chat'), identity: 'session.mode.chat',
        action: () => { if (valid()) this.phoneBack(); }});
      const labels = ['files', 'git', 'terminal', 'session_tools_terminal_screen', 'preview'];
      const identities = ['files', 'git', 'terminal', 'terminal_screen', 'preview'];
      for (const pick of [2, 1, 0, 3, 4]) {
        sheets.push({title: tr(labels[pick]), identity:'session.mode.' + identities[pick], action: () => {
          if (!valid()) return;
          if (name === 'tools') owner.selectWorkspaceTab(pick);
          else this.openTools('', pick);
        }});
      }
    }
    const main = this.model.mainActivityRow();
    if (main && name === 'conversation' && owner.target(owner.selected) === this.model.target(main)) {
      sheets.push({ title: tr('background_activity'), identity: 'background-activity-menu', action: () => { if (valid()) this.openActivity(); } });
    }
    if (owner.isChat() && owner.selected.machine) sheets.push({ title: tr('interaction_history'), action: () => { if (valid()) this.openHistory(); } });
    const computer = this.selectedComputer();
    if (computer) sheets.push({ title: tr('computer_screen_open'), action: () => { if (valid()) this.openComputer(computer); } });
    if (owner.selected.machine && (this.wide() || name === 'conversation')) sheets.push({ title: tr('workspace'), action: () => { if (valid()) this.openTools(); } });
    const canEdit = owner.connected && !owner.demo;
    if (owner.selected.kind !== 'session' || canEdit && sessionModeAvailable(owner.selected, 'permission_mode')) sheets.push({ title: tr('permissions'), action: () => { if (valid()) this.openSettings('session_permissions', true); } });
    sheets.push({ title: tr('voice'), action: () => { if (valid()) this.openSettings('voice', true); } });
    if (canEdit && sessionModeAvailable(owner.selected, 'effort')) sheets.push({ title: tr('effort'), action: () => { if (valid()) this.openSettings('session_effort', true); } });
    if (canEdit && sessionModeAvailable(owner.selected, 'model')) sheets.push({ title: tr('model'), action: () => { if (valid()) this.openSettings('session_model', true); } });
    if (owner.isChat() && owner.messages.length > 0) {
      sheets.push({ title: tr('export_chat'), identity: 'chat.actions.export', action: () => { if (valid()) this.openChatExport(); } });
    }
    if (owner.selected.kind === 'session' && owner.canAutoRenameAgent(owner.selected)) {
      sheets.push({
        title: tr(owner.isAutoRenamingRow(owner.selected) ? 'agent_renaming' : 'agent_auto_rename'),
        identity: 'chat.actions.autoname',
        action: () => { if (valid() && !owner.isAutoRenamingRow(owner.selected)) { void this.triggerDetailAutoRename(owner); } }
      });
    }
    const sid = owner.selected.session || owner.selected.id;
    const isMainChat = owner.selected.kind === 'conversation' && sid === owner.mainId && (!owner.mainMachineId || owner.selected.machine === owner.mainMachineId);
    if (owner.connected && owner.isChat() && !isMainChat && owner.selected.machine && (owner.demo || Wire.bool(owner.selected.data(), 'can_fork'))) {
      sheets.push({ title: tr('fork_continue_new_chat'), identity: 'chat.actions.fork', action: () => { if (valid()) { void this.forkCurrentChat(); } } });
    }
    const identity = JSON.stringify([owner.scope(), owner.connectionGeneration(), owner.target()]);
    if (owner.canStop() && this.inlineStopIdentity !== identity) sheets.push({ title: tr('stop'), action: () => { if (valid()) owner.stop(valid); } });
    this.chatMenuRevision++; this.chatMenuActions = sheets;
    const body = this.mainDestination(name, param) || this.hideChatTitle(name, param);
    this.chatTitleMenuVisible = !body; this.chatBodyMenuVisible = body;
  }
  private syncConversationRows(): void {
    const scope = JSON.stringify([this.model.scope(), this.model.profile.id, this.model.profile.manager]);
    this.sidebarTaskHistory.refresh(this.model,this.spacesShown || this.chatSearchShown && !this.startSearchInAgents || this.wide() && !this.collapsed);
    const groups=assistantSidebarChats(this.model.conversations,this.model.mainId,this.model.mainMachineId,Wire.parse(this.model.sidebarConfig),
      this.sidebarTaskHistory.known(this.model.scope()),this.model.profile.id,this.model.profile.manager);
    this.directionRowBindings = reconcileBoundRows(this.directionRowBindings, groups.directions,
      (row: ResourceRow) => row.key(), this.conversationBindingOwner, this.model, this.conversationBindingScope, scope);
    this.conversationRowBindings = reconcileBoundRows(this.conversationRowBindings, groups.recent,
      (row: ResourceRow) => row.key(), this.conversationBindingOwner, this.model, this.conversationBindingScope, scope);
    this.conversationBindingOwner = this.model;
    this.conversationBindingScope = scope;
  }
  private conversationDetail(row: ResourceRow): string {
    return row.kind === 'session' ? [this.model.machineName(row.machine), this.model.shortPath(row.path), row.state ? tr(row.state) : ''].filter((value: string) => value.length > 0).join(' · ') : row.detail;
  }
  @Builder drawerChat(binding: BoundRow<ResourceRow>, prefix: string) {
    if (this.matchesDrawer(binding.value.title)) {
      if (this.wide()) {
        ListItem() { this.conversationRow(binding,true) }
          .onClick(() => { if (this.drawerAcceptsTap()) this.openPickedChat(binding.value); }).id(prefix+binding.value.id)
      } else {
        ListItem() {
          Row({space:12}) {
            AssistantChatTitle({title:sidebarConversationTitle(binding.value,tr),source:assistantChatSource(binding.value,this.sidebarTaskHistory.known(this.model.scope())),
              gap:12,identity:'drawer.mark.'+binding.value.id}).layoutWeight(1)
            AssistantChatStateMark({state:assistantChatState(binding.value),identity:'drawer.mark.'+binding.value.id})
          }.width('100%').constraintSize({minHeight:this.drawerMetric(44)}).padding({left:12,right:12})
        }.accessibilityGroup(true).accessibilityText(sidebarConversationLabel(binding.value,this.sidebarTaskHistory.known(this.model.scope()),
          this.model.language === 'default' ? i18n.System.getSystemLanguage() : this.model.language,Date.now(),tr))
          .onClick(() => { if (this.drawerAcceptsTap()) this.openPickedChat(binding.value); }).id(prefix+binding.value.id)
      }
    }
  }

  @Builder conversationRow(binding: BoundRow<ResourceRow>, showDetail: boolean) {
    Row({ space: Tokens.inset }) {
      SymbolGlyph(Symbols.spaces).fontSize(Tokens.symbolFont).fontColor([Colors.secondary]).width(Tokens.symbol)
      Column({ space: Tokens.fine }) {
        Text(binding.value.title || binding.value.id).fontSize(Tokens.body).fontColor(Colors.ink).width('100%').maxLines(2).textOverflow({ overflow: TextOverflow.Ellipsis })
        if (showDetail && this.conversationDetail(binding.value)) Text(this.conversationDetail(binding.value)).fontSize(Tokens.meta).fontColor(Colors.secondary).width('100%').maxLines(2).textOverflow({ overflow: TextOverflow.Ellipsis })
      }.layoutWeight(1).alignItems(HorizontalAlign.Start)
    }.padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: Tokens.inset }).constraintSize({ minHeight: Tokens.touch }).width('100%')
  }
  @Builder state() {
    if (this.model.busy) LoadingProgress().height(Tokens.touch).width(Tokens.touch)
    if (this.model.error) {
      ListItem() { Text(trError(this.model.error)).fontSize(Tokens.meta).padding(Tokens.page) }
    }
  }
  private retryRoot(): void {
    if (this.navigationRestoredScope !== this.model.scope()) { this.activeScope = ''; this.model.error = ''; this.restoreScope(); }
    else if (this.model.connected) { this.model.error = ''; this.defaultMainAttempt = ''; this.model.refresh().then(() => this.ensureMainDestination()); }
    else this.model.foregrounded();
  }
  @Builder connectionState() {
    Row({ space: Tokens.gap }) { Text(trError(this.model.error)).fontSize(Tokens.meta).layoutWeight(1)
      Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry')).bindTips(tr('retry')).onClick(() => this.retryRoot())
    }.padding(Tokens.inset)
  }
  @Builder personaList() {
    List({ space: Tokens.section }) {
      if (this.model.error) ListItem() { this.connectionState() }
      ListItemGroup() {
      ListItem() { Row({ space: Tokens.inset }) { Image(this.look.avatarUri || $r('app.media.foreground')).width(Tokens.symbol).height(Tokens.symbol).objectFit(ImageFit.Contain); Text(tr('main_chat')).fontSize(Tokens.body).fontWeight(FontWeight.Medium).fontColor(Colors.ink).layoutWeight(1) }.padding({ left: Tokens.page, right: Tokens.page, top: Tokens.inset, bottom: Tokens.inset }).constraintSize({ minHeight: Tokens.header }).width('100%') }.selectable(true).selected(this.chatSelectionShown('conversation') && this.detailModel === this.model && this.detailModel.selected.id === this.model.mainId && this.detailModel.selected.kind === 'conversation' && (!this.model.mainMachineId || this.detailModel.selected.machine === this.model.mainMachineId))
        .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => { this.mainRowVisible = visible ? ratio : 0; const row = new ResourceRow(this.model.mainId, '', 'conversation'); row.manager = this.model.profile.manager; row.machine = this.model.mainMachineId; row.session = row.id; if (row.id && row.machine) { this.rowVisibility.set(this.model.scope() + this.model.target(row), visible ? ratio : 0); this.visibilityEpoch++; } })
        .onDisAppear(() => this.mainRowVisible = 0).onClick(() => this.openMain()).id('persona.main')
      ForEach(this.conversationRowBindings, (binding: BoundRow<ResourceRow>) => {
        ListItem() {
          this.conversationRow(binding, false)
        }.selectable(true).selected(this.chatSelectionShown('conversation') && this.detailModel === this.model && this.model.target() === this.model.target(binding.value))
          .onVisibleAreaChange([0, 1], (visible: boolean, ratio: number) => { this.rowVisibility.set(this.model.scope() + this.model.target(binding.value), visible ? ratio : 0); if (this.model.target() === this.model.target(binding.value)) this.visibilityEpoch++; })
          .onDisAppear(() => { this.rowVisibility.delete(this.model.scope() + this.model.target(binding.value)); this.visibilityEpoch++; }).onClick(() => this.openRow(binding.value))
      }, (binding: BoundRow<ResourceRow>) => binding.key)
      }.backgroundColor(Colors.canvas)
      ListItemGroup() {
      if (this.model.inboxPageVisible()) this.spaceRow('inbox')
      if (this.model.todosPageVisible()) this.spaceRow('todo')
      if (this.model.knowledgePageVisible()) this.spaceRow('knowledge')
      this.spaceRow('health')
      this.spaceRow('token_usage')
      if (this.applications.visible) this.spaceRow('applications')
      this.spaceRow('devices')
      this.spaceRow('settings')
      }.backgroundColor(Colors.canvas)
    }.width('100%').height('100%').backgroundColor(Colors.canvas).padding({ bottom: Tokens.listTail })
  }
  private activeModule(): string {
    if (!this.activeChild) return '';
    const names = this.stack.getAllPathName();
    for (let index = names.length - 1; index >= 0; index--) {
      const name = names[index];
      const value: Object | undefined = this.stack.getParamByIndex(index) as Object | undefined;
      const param = typeof value === 'string' ? value : '';
      if (name === 'resources') return param;
      if (['todo', 'knowledge', 'inbox'].includes(name)) return name;
      if (name === 'settings_root') return 'settings';
      if (name === 'settings') return this.settingsKind(param) === 'machines' ? 'devices' : 'settings';
      if (name === 'applications' || name === 'extension' && !!Wire.text(this.routeRow(param).data(), 'navigation_extension')) return 'applications';
      if (['conversation', 'session', 'assistant', 'tools', 'history'].includes(name)) return 'chat';
    }
    return '';
  }
  private chatSelectionShown(kind: string): boolean {
    return this.activeModule() === 'chat' && this.stack.getAllPathName().includes(kind);
  }
  private readingAgentDetail(): boolean {
    return this.model.section === 1 && this.detailModel.selected.kind === 'session' &&
      (!!this.activeChild || this.stack.getAllPathName().includes('session'));
  }
  private openAgentRepositoryRoute(kind: string, key: string): void {
    if (this.model.section !== 1) return;
    const tree = this.projectCatalog.tree(key);
    if (kind === 'start') {
      if (tree?.canStart) this.prepareAgent(undefined,tree);
    } else if (kind === 'project_tree' && tree ||
      kind === 'project' && this.projectCatalog.projects.some((project:PersonaProject) => project.key === key) ||
      kind === 'machine_projects' && this.projectCatalog.projects.some((project:PersonaProject) => project.hostKey === key)) {
      this.pushPlace(kind,key);
    }
  }
  @Builder agentsList() {
    AgentListPage({ projectCatalog:this.projectCatalog,
      onRepositoryOpen:(kind:string,key:string) => this.openAgentRepositoryRoute(kind,key),
      onRepositoryChanged:(tree:ProjectWorktree,change:WorktreeChange) => { this.worktreeChanged(tree,change,'agent_repositories',():boolean => this.model.section === 1 && this.agentWorkspace.groupingMode === 'repo' && (this.wide() || !this.stack.size())).catch((err:Error) => this.model.reportError(err)); },
      onOpenTodo:(row:ResourceRow) => this.openLinkedResource(row), onSearchMode:(editing:boolean)=>{this.agentSearching=editing;}, compactRoot:!this.wide() && !this.rail(), model: this.model, workspace: this.agentWorkspace, retainWorkspace: true, active: this.model.section === 1, selectedRow: this.detailModel.selected, selectionVisible: this.chatSelectionShown('session'), rail: this.rail(), readingDetail: this.readingAgentDetail(),
      onOpen: (row: ResourceRow) => this.openRow(row), onVisible: (row: ResourceRow, ratio: number) => {
        this.rowVisibility.set(this.model.scope() + row.key(), ratio); if (row.key() === this.detailModel.selected.key()) this.visibilityEpoch++;
      } })
  }
  @Builder inboxList() {
    InboxWorkspacePage({ model: this.model, visible: this.model.section === 2, phoneList:!this.wide(), refreshRevision: this.inboxRefreshRevision, toolbarActions:!this.wide() && this.rootModuleMenuKind()==='inbox', composeRevision:this.inboxComposeRevision, onComposeAvailability:(enabled:boolean) => { if (this.model.section===2 && !this.phoneTop) { this.inboxComposeScope=this.inboxPermissionOwner();this.inboxComposeAvailable=enabled; } }, onChanged: () => this.inboxRefreshRevision++, onOpen: (row: ResourceRow) => this.openRow(row), onOpenResource: (row: ResourceRow) => this.openLinkedResource(row), onLink: (address: string) => this.openReader(address),
      onHandoff: (row: ResourceRow) => this.openLinkedResource(row) })
  }
  private openFromPhoneSettings(page: string): void {
    if (page === 'standing_authorizations') {
      this.phoneSettingsShown = false;
      this.spacesShown = false;
      this.standingAuthSheetShown = true;
      return;
    }
    if (phoneSettingsLink(page)) {
      if (page === 'appearance' || page === 'phone_agent_layout' || page === 'phone_look_layout') this.ensureAgentWorkspace();
      this.phoneSettingsTrail = pushPhoneSetting(this.phoneSettingsTrail, page);
      return;
    }
    this.suspendPhoneSettings('settings');
    this.openSettings(page);
  }
  private suspendPhoneSettings(destination:string):void {
    this.phoneSettingsDepth = this.stack.size();
    this.phoneSettingsOwner = this.model.scope();
    this.phoneSettingsReturnRoute = destination;
    this.phoneSettingsShown = false;
    this.spacesShown = false;
  }
  private enterPhoneSettingsMachine(row:ResourceRow):void {
    const profile = this.model.profile;
    if (this.phoneSettingsShown && this.phoneSettingsPage === 'machines' && !this.phoneSettingsMachine &&
        row.id && row.group === profile.id && (!profile.manager || row.manager === profile.manager)) {
      // A new visit must have its own identity even if the directory reuses the same row.
      this.phoneSettingsMachine = NavigationContext.rowFrom(JSON.stringify(row));
    }
  }
  @Computed get phoneSettingsMachineTitle():string {
    const selected = this.phoneSettingsMachine;
    if (!selected) return '';
    return this.model.directoryMachines.find((row:ResourceRow) => row.key() === selected.key())?.title || selected.id;
  }
  private leavePhoneSettingsMachine(destination:string, origin:ResourceRow | undefined):boolean {
    if (!this.phoneSettingsShown || this.phoneSettingsPage !== 'machines' || this.phoneSettingsMachine !== origin) return false;
    this.suspendPhoneSettings(destination);
    return true;
  }
  private backPhoneSetting():void {
    if (this.phoneSettingsPage === 'machines' && this.phoneSettingsMachine) { this.phoneSettingsMachine = undefined; return; }
    if (this.phoneSettingsPage==='phone_inbox_sources' && this.inboxSourcesNav.back()) return;
    this.phoneSettingsTrail=this.phoneSettingsTrail.slice(0,-1);
  }
  @Monitor('detailModel.selected','detailModel.profile','detailModel.connected','detailModel.features','detailModel.sidebarConfig')
  updateDirectionPermissionRoutes():void {
    const owner=this.detailModel;
    if (this.directionPermissionsVisible && (!owner.connected || this.directionPermissionsTarget!==JSON.stringify([owner.scope(),owner.selected.key()]))) this.directionPermissionsVisible=false;
    if (owner.connected && owner.selected.kind==='conversation' && owner.features.includes('direction_sessions') && owner.repo().isDefaultManager())
      this.directionPermissionRoutes.bind(owner);
    else this.directionPermissionRoutes.close();
  }
  @Builder directionPermissionsSheet() {
    List() { PhoneDefaultPermissionSection({model:this.detailModel,elevated:true}) }
      .width('100%').height('100%').padding({left:16,right:16,top:20,bottom:24}).backgroundColor(Colors.sheetBackground)
      .id('settings.direction.default_permissions')
  }
  private pairFromSettings():void {
    if (this.phoneSettingsShown) {
      this.pairingSettingsOwner=this.model.scope();this.phoneSettingsShown=false;this.spacesShown=false;
    }
    this.openSheet('connect');
  }
  @Monitor('showSheet') restorePairingSettings():void {
    if (this.showSheet || !this.pairingSettingsOwner) return;
    const owner=this.pairingSettingsOwner;this.pairingSettingsOwner='';
    if (owner===this.model.scope() && !this.wide()) { this.spacesShown=true;this.phoneSettingsShown=true; }
  }
  private compactFormHeight(): number | SheetSize {
    if (!this.fullHeightPhone()) return SheetSize.LARGE;
    const ui = this.getUIContext(), env = this.environment;
    // Leave the status area visible and retain a margin above a taller native cutout.
    return ui.px2vp(env.windowHeightPx) - Math.max(Tokens.compactStatusInset, ui.px2vp(env.safeTopPx) + 8);
  }
  private fullHeightPhone(): boolean {
    const ui = this.getUIContext(), env = this.environment;
    const width = ui.px2vp(env.windowWidthPx), height = ui.px2vp(env.windowHeightPx);
    return env.isFullScreen && !env.nativeHoverAvailable && width > 0 && width < 600 && height >= 600 && height > width;
  }
  private compactNavigationInset(): number {
    if (!this.model.profile.id || !this.fullHeightPhone() || this.environment.safeTopPx <= 0) return 0;
    return Math.max(0, Tokens.compactStatusInset - this.getUIContext().px2vp(this.environment.safeTopPx));
  }
  private knowledgeSurface(): boolean {
    return this.phoneTop === 'resources' && this.phoneParam === 'knowledge' ||
      ['knowledge', 'knowledge_meetings', 'recordings'].includes(this.phoneTop);
  }
  private compactInsetEdges(): LayoutSafeAreaEdge[] {
    const bottom = this.model.profile.id && this.fullHeightPhone() && !this.environment.keyboardHeightPx &&
      !this.drawerFraction && !this.chatSearchShown && this.knowledgeSurface();
    return bottom ? [LayoutSafeAreaEdge.TOP, LayoutSafeAreaEdge.BOTTOM] : [LayoutSafeAreaEdge.TOP];
  }
  private compactInsetSurface(): ResourceColor {
    if (!this.model.profile.id || !this.fullHeightPhone()) return Color.Transparent;
    if (this.drawerFraction > 0) return Colors.phoneDrawerSurface;
    if (this.chatSearchShown && this.startSearchInAgents) return Colors.grouped;
    const page = this.phoneTop;
    if (this.knowledgeSurface() || page === 'resources' && (this.phoneParam === 'todo' || this.phoneParam === 'health') || page === 'project_tree' ||
      page === 'settings' && this.settingsKind(this.phoneParam) === 'machines') return Colors.grouped;
    if (!page && (this.projectPlace() ? this.projectSearching : this.model.section === 1)) return Colors.grouped;
    return Color.Transparent;
  }
  @Builder phoneMachineDirectory(selected:ResourceRow | undefined) {
    MachineDirectory({model:this.model,selected:selected,settingsScope:true,settingsHeading:!selected,
      active:this.phoneSettingsShown && this.phoneSettingsMachine === selected,
      onMachine:(row:ResourceRow) => this.enterPhoneSettingsMachine(row),
      onDetails:(row:ResourceRow) => {
        if (selected && row.key() === selected.key() && this.leavePhoneSettingsMachine('machine_settings',selected))
          this.pushPlace('machine_settings',JSON.stringify(row));
      },
      onManage:() => {
        if (this.leavePhoneSettingsMachine('machine_settings',undefined)) this.pushPlace('machine_settings','');
      },
      onSession:(row:ResourceRow) => {
        if (selected && row.group === selected.group && row.manager === selected.manager && row.machine === selected.id)
          this.openLinkedResource(row,(destination:string):boolean => this.leavePhoneSettingsMachine(destination,selected));
      }})
  }
  @Builder phoneSettings() {
    Column() {
      if (this.phoneSettingsPage || !this.phoneSettingsQuery.trim()) Row() {
        if (this.phoneSettingsPage) PhoneIconButton({symbol:'navigationBack',
          label:this.phoneSettingsPage === 'machines' ? tr(this.phoneSettingsMachine ? 'phone_machines' : 'settings') : tr('back'),identity:'settings.back',
          action:() => { this.backPhoneSetting(); }})
        else PhoneIconButton({symbol:'navigationClose',label:tr('close'),identity:'settings.close',
          action:() => { this.phoneSettingsQuery = ''; this.phoneSettingsShown = false; }})
        Text(this.phoneSettingsPage === 'machines' ? this.phoneSettingsMachineTitle :
          this.phoneSettingsPage==='phone_inbox_sources' ? this.inboxSourcesNav.title || tr('phone_inbox_sources') :
          this.phoneSettingsPage === 'appearance' ? '' : this.phoneSettingsPage === 'phone_language_options' ? tr('language') : this.phoneSettingsPage ? tr(this.settingsLabel(this.phoneSettingsPage)) : tr('settings'))
          .layoutWeight(1).textAlign(TextAlign.Center).fontSize(18).maxFontScale(Tokens.interfaceScaleFor(18)).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
        Blank().width(PhoneStyle.bar)
      }.width('100%').padding({left:PhoneStyle.page,right:PhoneStyle.page,top:16,bottom:10})
      if (this.phoneSettingsPage === 'machines') {
        Stack() {
          Column() { this.phoneMachineDirectory(undefined) }.width('100%').height('100%')
            .visibility(this.phoneSettingsMachine ? Visibility.Hidden : Visibility.Visible)
          if (this.phoneSettingsMachine) this.phoneMachineDirectory(this.phoneSettingsMachine)
        }.width('100%').layoutWeight(1)
      } else if (phoneAppearancePage(this.phoneSettingsPage)) {
        SettingsPage({model:this.model,page:'appearance',phoneAppearanceRoute:this.phoneSettingsPage,active:this.phoneSettingsShown,
          agentWorkspace:this.agentWorkspace,onOpen:(key:string) => this.openFromPhoneSettings(key)}).layoutWeight(1)
      } else if (this.phoneSettingsPage === 'phone_knowledge_selection') {
        PhoneKnowledgeSelection({model:this.model}).layoutWeight(1)
      } else if (this.phoneSettingsPage === 'phone_meeting_options') {
        PhoneMeetingSettings({model:this.model}).layoutWeight(1)
      } else if (this.phoneSettingsPage==='phone_settings_advanced') {
        SettingsDirectory({advanced:true,elevated:true,model:this.model,label:(key:string):string=>this.settingsLabel(key),
          onOpen:(key:string)=>this.openFromPhoneSettings(key)}).layoutWeight(1)
      } else if (this.phoneSettingsPage==='phone_inbox_sources') {
        PhoneInboxSources({model:this.model,navigation:this.inboxSourcesNav,onAsk:(text:string)=>this.askFromInboxSources(text),
          onWeb:(address:string)=>this.openReader(address)}).layoutWeight(1)
      } else if (this.phoneSettingsPage==='phone_default_permissions') {
        List() { PhoneDefaultPermissionSection({model:this.model,elevated:true}) }.width('100%').layoutWeight(1).padding(16)
      } else if (this.phoneSettingsPage==='phone_native_alarms') {
        PhoneAlarmsSettings({elevated:true,active:this.phoneSettingsShown}).layoutWeight(1)
      } else if (this.phoneSettingsPage==='phone_xiaoyi') {
        XiaoYiSettingsPage({model:this.model,elevated:true}).layoutWeight(1)
      } else if (['phone_hubs','phone_pairing','about','storage','phone_live_screen','phone_health_reports'].includes(this.phoneSettingsPage)) {
        PhoneSettingsDevicePages({model:this.model,elevated:true,page:this.phoneSettingsPage,
          onOpen:(key:string)=>this.openFromPhoneSettings(key),onPair:()=>this.pairFromSettings()}).layoutWeight(1)
      } else if (this.phoneSettingsPage) {
        PhoneSettingsOptions({elevated:true,model:this.model,page:this.phoneSettingsPage,label:(key:string):string => this.settingsLabel(key),
          onOpen:(key:string) => this.openFromPhoneSettings(key)}).layoutWeight(1)
      } else SettingsDirectory({elevated:true,model:this.model,ownerName:this.model.profile.name || tr('persona'),avatar:this.look.avatarUri,
        initialQuery:this.phoneSettingsQuery,onQueryChange:(value:string) => this.phoneSettingsQuery = value,
        showDismissSearch:true,onDismissSearch:() => { this.phoneSettingsQuery = ''; this.phoneSettingsShown = false; },
        label:(key:string):string => this.settingsLabel(key),onOpen:(key:string) => this.openFromPhoneSettings(key)}).layoutWeight(1)
    }.width('100%').height('100%').backgroundColor(Colors.sheetBackground).id('settings.sheet')
  }
  @Builder settingsList() {
    SettingsDirectory({model:this.model,ownerName: this.model.profile.name || tr('persona'), avatar: this.look.avatarUri,
      label: (key: string): string => this.settingsLabel(key), onOpen: (key: string) => this.openSettings(key)})
  }
  private async worktreeChanged(tree: ProjectWorktree, change: WorktreeChange, origin: string, atOrigin: () => boolean): Promise<void> {
    const root = this.model, scope = root.scope(), epoch = root.connectionGeneration(), stack = this.stack;
    const names = stack.getAllPathName(), intent = this.navigationIntent, section = this.model.section;
    const source = new ResourceRow(); source.group = tree.group; source.manager = tree.manager;
    const owner = root.directoryOwnerFor(source), ownerEpoch = owner?.connectionGeneration(), ownerScope = owner?.scope();
    const live = (): boolean => root.owns(epoch, scope) && this.model === root;
    const stay = (): boolean => live() && this.stack === stack && this.navigationIntent === intent && this.model.section === section && atOrigin();
    const fromDetail = origin === 'project_tree', browser = origin === 'agent_repositories' ? this.projectCatalog.agentBrowser : this.projectCatalog.browser;
    const created = change.created;
    if (created) {
      if (!created.receiptCurrent?.()) return;
      const available = (): boolean => {
        const machine = root.directoryMachines.find((row:ResourceRow) => row.group === created.group &&
          row.manager === created.manager && row.id === created.machine);
        const known = this.projectCatalog.tree(created.key);
        return !!machine && this.agentMachineOnline(machine) && (!known || known.canStart && known.matchesCreated(created));
      };
      if (owner && available() && stay() && !this.showSheet) {
        this.projectCatalog.selectCreated(created);
        browser.selectedTree = created.key; browser.selectedSession = '';
        if (fromDetail) this.pushPlace('project_tree',created.key);
        const childIntent = this.navigationIntent;
        const stillSelected = (): boolean => live() && created.receiptCurrent?.() === true && available() && !!this.projectCatalog.selectedWorktree(created.key) && this.stack === stack &&
          this.navigationIntent === childIntent && this.model.section === section &&
          (fromDetail ? this.stack.getAllPathName().slice(-1)[0] === 'project_tree' && this.stack.getParamByIndex(this.stack.size()-1) === created.key :
            atOrigin() && browser.selectedTree === created.key);
        void this.prepareAgent(undefined,created,false,stillSelected);
      }
    }
    if (owner && (change.action === 'create' || change.action === 'clean')) {
      try { await this.projectCatalog.saveParent(owner, tree, change.id, change.action === 'create' ? change.path : '', change.parent); }
      catch (_) { /* The host mutation succeeded; its inventory remains authoritative. */ }
    }
    if (!live()) return;
    if (change.action === 'clean' && this.projectCatalog.createdSelection?.key === tree.key) this.projectCatalog.createdSelection = undefined;
    if (change.action === 'clean' && stay()) {
      const index = names.findIndex((name: string, i: number) => name === 'project_tree' && stack.getParamByIndex(i) === tree.key);
      if (index === 0) stack.clear(); else if (index > 0) stack.popToIndex(index - 1);
      this.savePath();
    }
    this.projectCatalog.notice = tr(change.action === 'create' ? 'tree_created' : change.action === 'rename' ? 'tree_renamed' : change.branchRetained ? 'tree_cleaned_branch' : 'tree_cleaned');
    await this.projectCatalog.refresh(root);
    if (change.action === 'create' && !created && change.id !== tree.sourceId && change.parent === tree.sourceId && owner?.connected && ownerEpoch !== undefined && ownerScope !== undefined && owner.owns(ownerEpoch,ownerScope) && stay()) {
      const key = JSON.stringify([tree.group, tree.manager, tree.machine, tree.source, change.id]), actual = this.projectCatalog.tree(key);
      if (actual && actual.cwd === change.path && actual.branch === change.branch) {
        browser.selectedTree = actual.key; browser.selectedSession = '';
        if (fromDetail) this.pushPlace('project_tree',key);
      }
    }
  }
  // Selection marks the detail open beside the repository column. A stack navigation has no adjacent detail.
  @Computed get adjacentProjectItem(): string {
    if (!this.wide() || this.activeChild !== this.childKey(this.shownName, this.shownParam)) return '';
    if (this.shownName === 'project_tree') return 'tree:' + this.shownParam;
    if ((this.shownName === 'session' || this.shownName === 'tools') && this.targetReady(this.shownName, this.shownParam) &&
      this.detailModel.selected.kind === 'session') return 'session:' + this.detailModel.selected.key();
    return '';
  }
  @Builder projectPage(name: string, param: string) {
    ProjectsPage({ model: this.model, catalog: this.projectCatalog, searchWorkspace: this.agentWorkspace, page: name, target: param as string,
          groupedSearchRows:(workspace:AgentWorkspace,tasks:AgentTaskGrouping,decisions:AgentDecisionQueue):void => this.projectAgentMatches(workspace,tasks,decisions),
          compact: !this.wide(), adjacentItem: this.adjacentProjectItem, onBack: () => this.phoneBack(), onSearchMode: (active: boolean) => { this.projectSearching = active; },
          isMuted: (row: ResourceRow): boolean => this.agentWorkspace.isMuted(row),
          topContentInset: name === 'projects' && this.floatingProjectHeader ? Math.max(this.peerActionSize(), this.peerSegmentHeight()+8) + 4 : 0,
          onMachine: (tree: ProjectWorktree) => this.pushPlace('machine_projects', JSON.stringify([tree.group,tree.manager,tree.machine])),
          onProject: (key: string) => this.pushPlace('project', key),
          onTree: (key: string) => this.pushPlace('project_tree', key),
          onSession: (row: ResourceRow) => this.openLinkedResource(row),
          onStart: (tree: ProjectWorktree) => this.prepareAgent(undefined, tree),
          onChanged: (tree: ProjectWorktree, change: WorktreeChange) => { this.worktreeChanged(tree, change, name, (): boolean => name === 'projects' ? this.projectPlace() && (this.wide() || !this.stack.size()) :
            this.phoneTop === name && this.phoneParam === param).catch((err: Error) => this.model.reportError(err)); },
          onRefresh: () => this.projectCatalog.refresh(this.model) })
  }
  @Builder projectAgentMatches(workspace:AgentWorkspace,tasks:AgentTaskGrouping,decisions:AgentDecisionQueue) {
    AgentListPage({model:this.model,workspace:workspace,taskGrouping:tasks,decisions:decisions,projectCatalog:this.projectCatalog,externalSearchControls:true,
      compactRoot:true,retainWorkspace:true,active:this.projectPlace(),selectedRow:this.detailModel.selected,
      onOpen:(row:ResourceRow) => this.openLinkedResource(row),onOpenTodo:(row:ResourceRow) => this.openLinkedResource(row)})
  }
  @Builder railSpaceButton(kind: string) {
    ListItem() {
      Button({ type: ButtonType.Circle }) {
        SymbolGlyph(this.spaceSymbol(kind))
          .fontSize('18vp')
          .fontColor([this.drawerPlaceSelected(kind) ? Colors.link : Colors.ink])
      }
      .width(40)
      .height(40)
      .padding(0)
      .backgroundColor(this.drawerPlaceSelected(kind) ? Colors.selection : Colors.surface)
      .border({
        width: this.drawerPlaceSelected(kind) ? 1.5 : PhoneStyle.surfaceEdgeWidth(0.5),
        color: this.drawerPlaceSelected(kind) ? Colors.link : Colors.divider,
        radius: 20
      })
      .accessibilityText(this.spaceTitle(kind))
      .accessibilitySelected(this.drawerPlaceSelected(kind))
      .bindTips(this.spaceTitle(kind))
      .onClick(() => { Haptics.selection(); this.openSpace(kind); })
      .id('shell.rail.space.' + kind)
    }.width('100%').padding({ top: 4, bottom: 4 })
  }
  @Builder personaRail() {
    List() {
      ListItem() {
        Button({ type: ButtonType.Circle }) {
          Image(this.look.avatarUri || $r('app.media.foreground'))
            .width(34).height(34).objectFit(ImageFit.Cover).borderRadius(17)
        }
        .width(40)
        .height(40)
        .padding(0)
        .backgroundColor(Colors.surface)
        .border({
          width: this.chatSelectionShown('conversation') && this.detailModel.selected.id === this.model.mainId ? 2 : PhoneStyle.surfaceEdgeWidth(0.5),
          color: this.chatSelectionShown('conversation') && this.detailModel.selected.id === this.model.mainId ? Colors.link : Colors.divider,
          radius: 20
        })
        .shadow(PhoneStyle.cardElevation(this.environment.dark))
        .accessibilityText(tr('main_chat'))
        .bindTips(tr('main_chat'))
        .onClick(() => { Haptics.selection(); this.openMain(); })
        .id('shell.rail.main')
      }.width('100%').padding({ top: 4, bottom: 4 })
      if (this.model.inboxPageVisible()) this.railSpaceButton('inbox')
      if (this.model.todosPageVisible()) this.railSpaceButton('todo')
      if (this.model.knowledgePageVisible()) this.railSpaceButton('knowledge')
      this.railSpaceButton('health')
      this.railSpaceButton('devices')
      this.railSpaceButton('settings')
    }
    .width('100%')
    .height('100%')
    .alignListItem(ListItemAlign.Center)
    .scrollBar(BarState.Off)
    .padding({ top: 6, bottom: 12 })
    .backgroundColor(Colors.grouped)
    .id('shell.rail.persona_list')
  }
  @Builder railShell() {
    Column() {
      Column({ space: 2 }) {
        Button({ type: ButtonType.Circle }) {
          SymbolGlyph(Symbols.persona)
            .fontSize('17vp')
            .fontColor([this.peer() === 0 && !this.projectPlace() ? Colors.onAction : Colors.secondary])
        }
        .width(36)
        .height(34)
        .padding(0)
        .backgroundColor(this.peer() === 0 && !this.projectPlace() ? Colors.action : Color.Transparent)
        .accessibilityText(tr('persona'))
        .accessibilitySelected(this.peer() === 0 && !this.projectPlace())
        .bindTips(tr('persona'))
        .onClick(() => { Haptics.selection(); this.selectPeer(0); })
        .id('shell.rail.persona')

        Stack({ alignContent: Alignment.TopEnd }) {
          Button({ type: ButtonType.Circle }) {
            SymbolGlyph(Symbols.agents)
              .fontSize('17vp')
              .fontColor([this.peer() === 1 && !this.projectPlace() ? Colors.onAction : Colors.secondary])
          }
          .width(36)
          .height(34)
          .padding(0)
          .backgroundColor(this.peer() === 1 && !this.projectPlace() ? Colors.action : Color.Transparent)
          .accessibilityText(tr('agents'))
          .accessibilitySelected(this.peer() === 1 && !this.projectPlace())
          .bindTips(tr('agents'))
          .onClick(() => { Haptics.selection(); this.selectPeer(1); })
          .id('shell.rail.agents')
          if (this.waitingAgentCount > 0 && this.peer() !== 1) {
            Circle().width(8).height(8).fill(Colors.needs).stroke(Colors.surface).strokeWidth(1.5)
              .offset({ x: -3, y: 3 }).hitTestBehavior(HitTestMode.None).accessibilityLevel('no')
          }
        }
      }
      .padding(4)
      .margin({ top: 10, bottom: 8 })
      .backgroundColor(Colors.surface)
      .borderRadius(22)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: Colors.divider, radius: 22 })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .id('shell.rail.peer')

      Divider().strokeWidth(0.5).color(Colors.divider).width(32)

      Column() {
        if (this.peer() === 1 && !this.projectPlace()) this.agentsList()
        else this.personaRail()
      }.width('100%').layoutWeight(1)

      Divider().strokeWidth(0.5).color(Colors.divider).width(32)

      if (this.model.profile.id) {
        Column() {
          this.quickPaletteButton(40)
        }.padding({ top: 10 })
      }

      Button({ type: ButtonType.Circle }) {
        SymbolGlyph(Symbols.expand).fontSize('16vp').fontColor([Colors.ink])
      }
      .width(40)
      .height(40)
      .padding(0)
      .margin({ top: 10, bottom: 14 })
      .backgroundColor(Colors.surface)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: Colors.divider, radius: 20 })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .accessibilityText(tr('expand'))
      .bindTips(tr('expand'))
      .onClick(() => this.expandRail())
      .id('shell.rail.expand')
    }
    .width('100%')
    .height('100%')
    .alignItems(HorizontalAlign.Center)
    .backgroundColor(Colors.grouped)
    .id('shell.rail')
  }
  @Builder primaryContent() {
    if (this.rail()) {
      this.railShell()
    } else if (this.projectPlace()) {
      ForEach([this.projectRootRevision], (_revision: number) => { this.projectPage('projects', '') }, (revision: number) => String(revision))
    }
    else if (this.peer() === 1) this.agentsList()
    else this.personaList()
  }
  @Builder workspaceSheet() {
    List() {
      ForEach(this.model.profiles, (profile: HubProfile) => { ListItem() { Text(profile.name).padding(Tokens.page) }
        .onClick(() => { this.model.selectProfile(profile); this.showSheet = false; }) }, (profile: HubProfile) => profile.id)
      ForEach(this.model.managers, (manager: ResourceRow) => { ListItem() { Text(manager.title).padding(Tokens.page) }
        .onClick(() => { this.model.selectManager(manager.id); this.showSheet = false; }) }, (manager: ResourceRow) => manager.id)
      ListItem() { Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('connect')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('connect', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('connect')).bindTips(tr('connect')).onClick(() => { this.showSheet = false; this.openSheet('connect'); }) }
    }.width('100%')
  }
  private async scanPairing(): Promise<void> {
    const host = this.getUIContext().getHostContext(); if (!host || this.scanning) return;
    const revision = ++this.scanRevision, generation = this.model.connectionGeneration(), scope = this.model.scope(); this.scanning = true;
    try {
      const result = await scanBarcode.startScanForResult(host, { scanTypes: [scanCore.ScanType.QR_CODE], enableAlbum: true, enableMultiMode: false });
      if (revision !== this.scanRevision || !this.showSheet || this.sheetKind !== 'connect' || !this.model.owns(generation, scope)) return;
      Pairing.parse(result.originalValue); this.address = result.originalValue; this.pairCode = ''; this.model.error = '';
      this.pairManually = true;
      if (this.compactPairing()) await this.connect();
    } catch (error) { if (revision === this.scanRevision && (error as BusinessError).code !== 1000500002) this.model.error = 'connection_failed'; }
    finally { if (revision === this.scanRevision) this.scanning = false; }
  }
  @Builder pairingForm() {
    Column({ space: Tokens.page }) {
      Row({ space: Tokens.gap }) {
        TextInput({ text: this.address, placeholder: tr('hub_address') }).onChange((value: string) => this.address = value).id('pair.address').layoutWeight(1)
        if (canIUse('SystemCapability.Multimedia.Scan.ScanBarcode')) Button({ type: ButtonType.Circle }) {
          SymbolGlyph($r('sys.symbol.qrcode')).fontSize(Tokens.symbolFont).fontColor([Colors.link])
        }.attributeModifier(ButtonStyles.chrome).accessibilityText(tr('scan_qr')).enabled(!this.scanning && !this.model.busy).onClick(() => this.scanPairing()).id('pair.scan')
      }.width('100%')
      TextInput({ text: this.pairCode, placeholder: tr('pair_code') }).type(InputType.Password).onChange((value: string) => this.pairCode = value).id('pair.code')
      if (this.model.error) Text(trError(this.model.error)).fontColor($r('app.color.persona_danger'))
      Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('verify')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('verify', true)]) }.attributeModifier(ButtonStyles.iconPrimary).accessibilityText(tr('verify')).bindTips(tr('verify')).enabled(!!this.address.trim() && (!!this.pairCode.trim() || isPairingAddress(this.address.trim())) && !this.model.busy).onClick(() => this.connect()).id('pair.verify').alignSelf(ItemAlign.End)
    }.padding(Tokens.page).width('100%')
  }
  private agentActionBottom(): number {
    const env = this.environment;
    if (!this.fullHeightPhone()) return 12;
    if (env.keyboardHeightPx > 0) return 16;
    const bottom = env.safeBottomPx > 0 ? this.getUIContext().px2vp(env.windowHeightPx - env.safeBottomPx) : 0;
    return Math.max(34, bottom) + Tokens.gap;
  }
  private createAgentKey(): string {
    if (this.creatingAgent) return 'sending';
    return this.agentModel.operationBlocked('/api/v1/sessions') ? 'result_unknown' : 'create';
  }

  private agentPickerTitle(): string {
    const keys: Record<string,string> = {'workspace':'flow_pick_tree','folder':'directory','launcher':'launcher',
      'model':'flow_model','effort':'flow_effort','preset':'phone_preset'};
    return tr(keys[this.agentPanel] || 'new_agent');
  }
  private effectiveLauncher(): string { return this.launcher || this.defaultLauncherName; }
  private chosenLauncher(): Record<string,Object> {
    return this.launcherData.find((v:Record<string,Object>) => Wire.text(v,'name') === this.effectiveLauncher()) || {};
  }
  private updateAgentEfforts(): void {
    const configured = this.agentUsesModelSettings && this.effectiveLauncher() === this.defaultLauncherName ? this.agentConfiguredModel : '';
    this.launchChoices.updateEfforts(this.agentModelId || configured);
  }
  private startAgentEffort(): string {
    return this.launchChoices.startEfforts(this.chosenLauncher()).includes(this.agentEffort) ? this.agentEffort : '';
  }
  private agentModelHelp(): string {
    const about = tr(this.agentUsesModelSettings ? 'flow_start_model_settings_hint' : 'flow_start_machine_launchers_hint');
    const choices = !this.launchChoices.locked && (this.launchChoices.acceptsTypedModel ||
      this.launchChoices.models.length > 0 || !!this.launchChoices.defaultModelName());
    return choices && !providerStartModes(this.chosenLauncher()) ? tr('flow_start_model_terminal_hint').replace('{about}',about) : about;
  }
  private promptOptional(): boolean {
    const choice = this.chosenLauncher();
    if (Wire.bool(choice,'prompt_optional')) return true;
    // Only known Codex launchers without the optional-prompt capability need a first message.
    const agent = Wire.text(choice,'agent') || Wire.text(choice,'agent_type') || Wire.text(choice,'name');
    return !!agent && agent !== 'codex';
  }
  private canStartAgent(): boolean {
    const machine = this.chosenAgentMachine(), tree = this.selectedAgentTree();
    return !!machine && this.agentOwnsMachine(machine) && this.agentMachineOnline(machine) && !this.creatingAgent && !this.switchingAgentMachine &&
      this.agentModel.connected && !this.agentModel.operationBlocked('/api/v1/sessions') &&
      (!!this.newTask.trim() || this.promptOptional() || !!this.chatForward.newFor(this.agentModel)) && this.chatForward.newAllows(this.agentModel,this.chosenLauncher()) && (!(this.agentWorktree || this.pickedAgentTree) || !!tree && tree.canStart && tree.cwd === this.directory) &&
      !this.blockedLaunchers.some((v:LaunchChoice) => v.id === this.effectiveLauncher()) &&
      (!this.launchListReady || !this.launcher || this.agentLauncherOptions.includes(this.launcher));
  }
  private pickerOptions(): LaunchChoice[] {
    if (this.agentPanel === 'workspace') return [new LaunchChoice('',tr('flow_choose_folder'))].concat(
      this.availableAgentTrees().filter((t:ProjectWorktree) => t.pickerWorkspace).map((t:ProjectWorktree) => new LaunchChoice(t.key,t.title,[t.branch,t.cwd].filter((v:string) => !!v).join(' · '))));
    if (this.agentPanel === 'launcher') return [new LaunchChoice('',tr('phone_launcher_default'),this.machineDefaultLabel)].concat(
      this.agentLauncherOptions.map((value:string,i:number) => new LaunchChoice(value,this.agentLauncherLabels[i] || value,
        Wire.text(this.launcherData.find((v:Record<string,Object>) => Wire.text(v,'name') === value) || {},'agent_type')))).concat(this.blockedLaunchers);
    if (this.agentPanel === 'preset') return [new LaunchChoice('',tr('phone_default_preset'))].concat(
      this.agentPresets.map((v:Record<string,Object>) => new LaunchChoice(Wire.text(v,'name'),Wire.text(v,'name'),
        [Wire.text(v,'launcher'),Wire.text(v,'model'),Wire.text(v,'effort')].filter((label:string) => !!label).join(' · '))));
    if (this.agentPanel === 'model') {
      const hint = this.startModelRow('').detail;
      const detail = [this.effectiveLauncher(),hint === tr('flow_use_default') ? '' : hint].filter((v:string) => !!v).join(' · ');
      return [new LaunchChoice('',tr('admin_default'),detail)].concat(this.launchChoices.locked ? [] : this.launchChoices.models);
    }
    if (this.agentPanel === 'effort') return [new LaunchChoice('',tr('flow_use_default'))].concat(
      this.launchChoices.startEfforts(this.chosenLauncher()).map((v:string) => new LaunchChoice(v,v)));
    return [];
  }
  private selectedPickerId(): string {
    if (this.agentPanel === 'workspace') return this.selectedAgentTree()?.key || '';
    if (this.agentPanel === 'launcher') return this.launcher;
    if (this.agentPanel === 'preset') return this.agentPreset;
    return this.agentPanel === 'model' ? this.agentModelId : this.agentEffort;
  }
  private openAgentPicker(kind:string): void {
    if (this.switchingAgentMachine || this.creatingAgent || !this.agentOwnsMachine(this.chosenAgentMachine()) ||
      kind === 'model' && !!this.launchChoices.locked || kind === 'effort' && this.launchChoices.startEfforts(this.chosenLauncher()).length === 0) return;
    this.startFailure = ''; this.agentPanel = kind;
    if (kind === 'workspace' && !this.projectCatalog.loading) this.projectCatalog.refresh(this.model);
    else if (kind === 'folder') this.browseAgentFolder(this.directory);
    else if (kind === 'model') this.loadAgentModels();
  }
  private loadAgentModels(): void {
    const machine = this.chosenAgentMachine(), launcher = this.effectiveLauncher(), form = this.agentFormRevision, owner = this.agentModel;
    if (!launcher || !machine || !this.agentOwnsMachine(machine)) return;
    this.launchChoices.loadModels(owner,machine.id,launcher).then(() => {
      if (form === this.agentFormRevision && owner === this.agentModel && this.chosenAgentMachine()?.key() === machine.key() &&
          this.effectiveLauncher() === launcher) this.updateAgentEfforts();
    });
  }
  private browseAgentFolder(path:string): void {
    const machine = this.chosenAgentMachine();
    if (machine && this.agentOwnsMachine(machine) && this.agentPanel === 'folder') this.launchChoices.folder(this.agentModel,machine.id,path);
  }
  private chooseAgentOption(value:string): void {
    const refreshModels = this.agentPanel === 'launcher' || this.agentPanel === 'preset';
    if (this.agentPanel === 'workspace') {
      const tree = this.availableAgentTrees().find((t:ProjectWorktree) => t.key === value);
      if (value && (!tree || !tree.pickerWorkspace)) return;
      this.pickedAgentTree = value;
      if (tree) this.directory = tree.cwd;
    } else if (this.agentPanel === 'folder') {
      if (value !== this.launchChoices.path || this.launchChoices.loading) return;
      this.pickedAgentTree = ''; this.directory = value;
    } else if (this.agentPanel === 'launcher') {
      this.launcher = value; this.agentPreset = ''; this.agentModelId = ''; this.agentEffort = ''; this.launchChoices.clear();
    } else if (this.agentPanel === 'preset') {
      const preset = this.agentPresets.find((v:Record<string,Object>) => Wire.text(v,'name') === value);
      this.agentPreset = value;
      if (preset) this.launcher = Wire.text(preset,'launcher');
      this.agentModelId = preset ? Wire.text(preset,'model') : ''; this.agentEffort = preset ? Wire.text(preset,'effort') : '';
      this.launchChoices.clear();
    } else if (this.agentPanel === 'model') {
      if (this.launchChoices.locked && value) return;
      this.agentPreset = ''; this.agentModelId = this.launchChoices.selectedModelId(value); this.updateAgentEfforts();
      this.agentEffort = this.startAgentEffort();
    } else if (this.agentPanel === 'effort') {
      const options = this.launchChoices.startEfforts(this.chosenLauncher());
      if (!options.length || value && !options.includes(value)) return;
      this.agentPreset = ''; this.agentEffort = value;
    }
    this.closeAgentPanel(); this.saveAgentDraft();
    if (refreshModels) this.loadAgentModels();
  }
  private closeAgentPanel(): void {
    if (this.startWorktree.busy) return;
    this.launchChoices.cancelRead(); this.startWorktree.close(); this.startTreeSource = undefined; this.agentPanel = '';
  }
  private makeChildFromAgent(): void {
    const tree = this.selectedAgentTree();
    if (!tree || !tree.permits('create')) return;
    this.startTreeSource = tree; this.agentPanel = 'worktree'; this.startWorktree.open(this.model,tree,'create');
  }
  private async selectNewAgentTree(check:boolean = false, replay:boolean = false): Promise<void> {
    const source = this.startTreeSource, owner = this.agentModel, form = this.agentFormRevision, root = this.model;
    if (!source) return;
    const change = check ? await this.startWorktree.reconcile(replay) : await this.startWorktree.submit();
    if (!change || form !== this.agentFormRevision || this.agentPanel !== 'worktree' || this.startTreeSource !== source) return;
    await this.projectCatalog.saveParent(owner,source,change.id,change.path,change.parent).catch(() => {});
    await this.projectCatalog.refresh(root);
    if (form !== this.agentFormRevision || this.agentPanel !== 'worktree' || this.startTreeSource !== source) return;
    const key = JSON.stringify([source.group,source.manager,source.machine,source.source,change.id]);
    this.pickedAgentTree = key; this.directory = change.path; this.closeAgentPanel(); this.saveAgentDraft();
    if (!this.projectCatalog.tree(key)) this.startFailure = 'project_gone';
  }
  private startModelRow(picked: string): LaunchChoice {
    const inherited = this.agentUsesModelSettings && this.effectiveLauncher() === this.defaultLauncherName ? this.agentConfiguredModel : '';
    return this.launchChoices.modelPresentation(picked,inherited,this.chosenLauncher(),!!this.launcher,tr);
  }
  private launchRowTitle(key:string): string {
    if (key === 'workspace') return tr('workspace');
    if (key === 'folder') return this.directory ? this.directory.split('/').filter((part:string) => !!part).pop() || '/' : tr('directory');
    if (key === 'preset') return this.agentPreset ? tr('phone_start_named_preset').replace('{preset}',this.agentPreset) : tr('phone_start_preset');
    if (key === 'launcher') return this.launcher || (this.machineDefaultLabel ? tr('admin_default')+' · '+this.machineDefaultLabel : tr('launcher'));
    if (key === 'model') return this.startModelRow(this.agentModelId).title;
    return tr('flow_effort');
  }
  private launchRowDetail(key:string): string {
    if (key === 'workspace') return this.selectedAgentTree()?.title || tr('flow_choose_folder');
    if (key === 'folder') return this.agentPathLabel(this.directory || Wire.text(this.chosenAgentMachine()?.data() || {},'home') || '~');
    if (key === 'preset') return this.agentTierProfile;
    if (key === 'launcher') return this.launcher ? Wire.text(this.chosenLauncher(),'model') : '';
    if (key === 'model') return this.startModelRow(this.agentModelId).detail;
    return this.agentEffort || tr('flow_use_default');
  }
  private agentSize(regular: number, largest: number): number {
    return PhoneStyle.formSize(regular,largest,this.environment.fontScale);
  }
  @Builder agentSectionTitle(key: string, followsGroup: boolean) {
    Row() {
      Text(tr(key)).fontSize(16).maxFontScale(Tokens.interfaceScaleFor(16)).fontWeight(600).fontColor(Colors.secondary)
        .maxLines(2).textOverflow({overflow:TextOverflow.Ellipsis})
    }.width('100%').height(this.agentSize(40.33,47.67)).padding({left:20,right:20,top:10})
      .margin({top:followsGroup ? 17.67 : 0}).id('agent.section.'+key)
  }
  @Builder workspaceOption() {
    Row({space:12}) {
      Text(tr('workspace')).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.ink)
        .layoutWeight(1).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
      Text(this.launchRowDetail('workspace')).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.secondary)
        .constraintSize({maxWidth:'60%'}).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
      if (!this.agentWorktree) PhoneOutlineIcon({name:'forward',iconScale:this.agentSize(1,1.35),color:Colors.secondary}).opacity(0.55).id('agent.chevron.workspace')
    }.width('100%').constraintSize({minHeight:this.agentSize(52.33,59.33)}).padding({left:20,right:20})
      .onClick(() => { if (!this.agentWorktree) this.openAgentPicker('workspace'); }).accessibilityGroup(true).id('agent.workspace')
  }
  private selectedTreeKind(tree: ProjectWorktree): string {
    const kind = tr(tree.workspaceKind === 'folder' ? 'phone_session_folder' : tree.main ? 'project_primary' : 'project_worktree');
    return kind+(tree.workspaceKind !== 'folder' && tree.branch && tree.branch !== tree.title ? ' · '+tree.branch : '');
  }
  @Builder agentOptionRow(key:string) {
    Row({space:10}) {
      Stack() {
        if (key === 'preset' || key === 'launcher') {
          PhoneOutlineIcon({name:'cube',iconScale:(key === 'preset' ? 1 : 0.82)*this.agentSize(1,1.35)})
        } else if (key === 'model') {
          PhoneOutlineIcon({name:'sparkles',iconScale:this.agentSize(1,1.35)})
        } else {
          SymbolGlyph(key === 'folder' ? $r('sys.symbol.folder_fill') : key === 'effort' ? Symbols.health : Symbols.apps)
            .fontSize(Tokens.interfaceFontVp(18,this.environment.fontScale)).fontColor([Colors.link])
        }
      }.width(this.agentSize(key==='preset' ? 30 : 21,key==='preset' ? 38 : 29)).id('agent.row_icon.'+key)
      Column({space:2}) {
        Text(this.launchRowTitle(key)).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17))
          .fontWeight(key==='folder' ? 600 : 400).fontColor(Colors.ink).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        if (this.launchRowDetail(key)) Text(this.launchRowDetail(key)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
          .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).ellipsisMode(key==='folder' ? EllipsisMode.START : EllipsisMode.END)
      }.alignItems(HorizontalAlign.Start).layoutWeight(1)
      if (key === 'model' && this.launchChoices.locked) {
        SymbolGlyph($r('sys.symbol.lock')).fontSize(Tokens.interfaceFontVp(15,this.environment.fontScale))
          .fontColor([Colors.secondary]).id('agent.model.lock')
      } else {
        PhoneOutlineIcon({name:'forward',iconScale:this.agentSize(1,1.35),color:Colors.secondary}).opacity(0.55).id('agent.chevron.'+key)
      }
    }.enabled(key !== 'model' || !this.launchChoices.locked).width('100%').constraintSize({minHeight:this.launchRowDetail(key) ? this.agentSize(66.67,81.33) : this.agentSize(52.33,59.33)})
      .padding({left:20,right:20,top:12,bottom:12}).onClick(() => this.openAgentPicker(key)).accessibilityGroup(true).id('agent.'+key)
  }
  @Builder launchDivider() { Divider().strokeWidth(0.5).color(Colors.divider).margin({left:48}) }
  private agentPathLabel(path:string): string {
    const machine = this.chosenAgentMachine(), home = machine ? Wire.text(machine.data(),'home').replace(/\/+$/,'') : '';
    return home && (path === home || path.startsWith(home + '/')) ? '~' + path.slice(home.length) : path;
  }
  private selectRecentDirectory(choice: LaunchChoice): void {
    if (this.agentWorktree || !this.recentAgentDirectories.some((row:LaunchChoice) => row.id === choice.id)) return;
    this.pickedAgentTree = ''; this.directory = choice.id; this.saveAgentDraft();
  }
  private recentUsed(choice: LaunchChoice): string {
    const locale = this.model.language === 'default' ? i18n.System.getSystemLanguage() : this.model.language;
    return relativeActivityDate(Date.parse(choice.description),Date.now(),locale,tr('agent_time_just_now'),tr('kb_minutes_ago'),tr('kb_yesterday'));
  }
  @Builder recentDirectory(choice: LaunchChoice) {
    Row({space:8}) {
      SymbolGlyph(this.directory === choice.id ? $r('sys.symbol.checkmark_circle_fill') : $r('sys.symbol.clock_arrow_2_circlepath'))
        .fontSize(Tokens.interfaceFontVp(18,this.environment.fontScale)).fontColor([this.directory === choice.id ? Colors.link : Colors.secondary])
        .width(this.agentSize(20,27))
      Column({space:2}) {
        Text(choice.title).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.ink)
          .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        Text(this.agentPathLabel(choice.id.substring(0,choice.id.lastIndexOf('/')) || '/')).fontSize(12)
          .maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1)
          .textOverflow({overflow:TextOverflow.Ellipsis}).ellipsisMode(EllipsisMode.START)
      }.alignItems(HorizontalAlign.Start).layoutWeight(1)
      if (this.recentUsed(choice)) Text(this.recentUsed(choice)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary)
        .maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).constraintSize({maxWidth:'40%'}).id('agent.recent_directory.time')
    }.width('100%').constraintSize({minHeight:this.agentSize(66.67,81.33)}).padding({left:20,right:20,top:12,bottom:12})
      .accessibilityGroup(true).accessibilitySelected(this.directory === choice.id)
      .onClick(() => this.selectRecentDirectory(choice)).id('agent.recent_directory')
  }
  private machineChoiceDetail(row:ResourceRow):string {
    if (!this.agentMachineOnline(row)) return tr('offline');
    const group = this.model.profiles.length > 1 ? this.model.groupName(row) : '';
    return group || Wire.text(row.data(),'hostname') || tr('online');
  }
  private focusAgentDraft(field:string):void {
    this.focusedAgentField = field;
    try { this.agentFocusScrollOrigin = this.agentFormScroll.currentOffset().yOffset; }
    catch (_) { this.agentFocusScrollOrigin = -1; }
    this.revealFocusedAgentDraft();
  }
  private agentFormWillScroll(source:ScrollSource):void {
    if (source === ScrollSource.DRAG || source === ScrollSource.SCROLL_BAR || source === ScrollSource.OTHER_USER_INPUT) {
      this.agentFocusScrollOrigin = -1;
      this.agentFocusLayoutRequest++;
    }
  }
  private revealFocusedAgentDraft(): void {
    const field = this.focusedAgentField, revision = this.agentFormRevision;
    if (!field || !this.showSheet || this.sheetKind !== 'newagent' || this.agentPanel) return;
    const request = ++this.agentFocusLayoutRequest;
    setTimeout(() => {
      if (this.agentFocusLayoutRequest !== request || this.focusedAgentField !== field || this.agentFormRevision !== revision || !this.showSheet ||
        this.sheetKind !== 'newagent' || this.agentPanel) return;
      try {
        const context = this.getUIContext(), components = context.getComponentUtils();
        const editor = components.getRectangleById(field), form = components.getRectangleById('agent.fields');
        const content = components.getRectangleById('agent.fields.content');
        const action = components.getRectangleById('agent.create');
        if (editor.size.height <= 0 || form.size.height <= 0 || content.size.height <= 0 || action.size.height <= 0) return;
        const available = context.px2vp(action.windowOffset.y - form.windowOffset.y) - 8;
        if (available <= 8) return;
        // The field and its content move together. Their distance remains valid when the
        // keyboard, focus and area callbacks see the same rendered frame more than once.
        const fieldTop = context.px2vp(editor.windowOffset.y - content.windowOffset.y);
        const atTop = fieldTop - 8;
        const atBottom = fieldTop + context.px2vp(editor.size.height) - available;
        const current = this.agentFormScroll.currentOffset().yOffset;
        const origin = this.agentFocusScrollOrigin >= 0 ? this.agentFocusScrollOrigin : current;
        // A multiline editor taller than this viewport can keep its own caret in view.
        const firstOffset = Math.min(atTop, atBottom), lastOffset = Math.max(atTop, atBottom);
        const reveal = Math.min(Math.max(origin, firstOffset), lastOffset);
        const maxOffset = Math.max(0, context.px2vp(content.size.height - form.size.height));
        const target = Math.max(0, Math.min(reveal, maxOffset));
        if (Math.abs(target - current) > context.px2vp(1)) {
          this.agentFormScroll.scrollTo({xOffset:0,yOffset:target});
        }
      } catch (_) { /* The sheet can disappear before the input method completes its layout. */ }
    }, 0);
  }
  private agentDraftAreaChanged(before:Area,after:Area):void {
    if (before.width!==after.width || before.height!==after.height) this.revealFocusedAgentDraft();
  }
  @Monitor('environment.fontScale','environment.keyboardHeightPx') agentDraftEnvironmentChanged():void { this.revealFocusedAgentDraft(); }

  private machineSymbolScale(row: ResourceRow): number {
    if (Wire.text(row.data(),'icon') !== 'laptop') return 1;
    // The laptop uses Swift's title2 symbol (22 to 28 pt). Keep the allocated card slot fixed.
    return this.agentSize(0.97,0.97*28/22)/this.agentSize(1,1.35);
  }
  private machineCardHeight(row: ResourceRow): number {
    return Wire.text(row.data(),'icon') === 'desktop' ? this.agentSize(99,120.67) : this.agentSize(96.67,117.67);
  }
  @Builder machineChoice(row:ResourceRow) {
    Button({type:ButtonType.Normal}) {
      Column({space:8}) {
        Row() {
          if (['server','laptop','desktop'].includes(Wire.text(row.data(),'icon'))) {
            // ArkUI's Shape can keep the old path transform when a mounted card changes its Dynamic Type size.
            ForEach([this.agentSize(1,1.35)],(iconScale:number) => {
              PhoneOutlineIcon({name:Wire.text(row.data(),'icon'),iconScale:iconScale,color:Colors.ink})
                .scale({x:this.machineSymbolScale(row),y:this.machineSymbolScale(row),centerX:0,centerY:0})
                .id('agent.machine_icon.'+row.id)
            },(iconScale:number):string => String(iconScale))
          } else {
            SymbolGlyph(Symbols.device).fontSize(Tokens.interfaceFontVp(22,this.environment.fontScale)).fontColor([Colors.ink])
          }
          Blank().layoutWeight(1)
          Circle().width(7).height(7).fill(this.agentMachineOnline(row) ? Colors.positive : Colors.secondary)
        }.width('100%')
        Text(row.title || row.id).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontWeight(600).fontColor(Colors.ink).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
        Text(this.machineChoiceDetail(row)).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis}).id('agent.machine_detail.'+row.id)
      }.width('100%').alignItems(HorizontalAlign.Start).padding(12)
    }.width(this.agentSize(144,181.67)).height(this.machineCardHeight(row)).padding(0).borderRadius(14)
      .backgroundColor(this.sameMachine(row,this.chosenAgentMachine()) ? Colors.selection : Colors.subtle)
      .border({width:this.sameMachine(row,this.chosenAgentMachine()) ? 1.5 : 0,color:Colors.link,radius:14})
      .enabled(this.agentMachineOnline(row) && (!this.agentWorktree || this.sameMachine(row,this.chosenAgentMachine())))
      .accessibilityGroup(true).accessibilityText((row.title || row.id)+' · '+this.machineChoiceDetail(row))
      .accessibilitySelected(this.sameMachine(row,this.chosenAgentMachine())).onClick(() => this.pickAgentMachine(row)).id('agent.machine')
  }
  private agentStartButtonHeight():number {
    return PhoneStyle.formSize(60,69.3,this.environment.fontScale);
  }
  private agentScrollClearance():number {
    const error = this.startFailure ? this.agentErrorHeight + Tokens.gap : 0;
    return 14 + 7 + this.agentStartButtonHeight() + this.agentActionBottom() + error;
  }
  @Builder agentStartFooter() {
    Column({space:Tokens.gap}) {
      if (this.startFailure) Text(trError(this.startFailure)).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14))
        .fontColor(Colors.danger).width('100%').padding(8).backgroundColor(Colors.sheetCard).borderRadius(PhoneStyle.cardRadius).id('agent.error')
        .onAreaChange((_old:Area,current:Area) => {
          const height=Number(current.height);
          if (Number.isFinite(height) && height >= 0) this.agentErrorHeight=height;
        })
      Button(tr(this.creatingAgent || this.agentModel.operationBlocked('/api/v1/sessions') ? this.createAgentKey() : 'flow_start_chat'),{type:ButtonType.Capsule})
        .height(this.agentStartButtonHeight()).width('100%').backgroundColor(Colors.action).fontColor(Colors.onAction)
        .borderWidth(Colors.systemControls ? 0.67 : 0).borderColor(Colors.prominentRim)
        .fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontWeight(FontWeight.Medium)
        .hitTestBehavior(HitTestMode.Block).enabled(this.canStartAgent()).onClick(() => this.createAgent()).id('agent.create')
    }.width('100%').height('100%').justifyContent(FlexAlign.End)
      .padding({left:24,right:24,top:7,bottom:this.agentActionBottom()}).hitTestBehavior(HitTestMode.Transparent)
  }
  @Builder newAgentForm() {
    if (this.agentPanel === 'worktree') {
      WorktreeSheet({form:this.startWorktree,onCancel:() => this.closeAgentPanel(),onRetry:() => this.makeChildFromAgent(),onSubmit:() => this.selectNewAgentTree(),onReconcile:(replay:boolean) => this.selectNewAgentTree(true,replay)})
    } else if (this.agentPanel) {
      AgentStartPicker({kind:this.agentPanel,choices:this.launchChoices,heading:this.agentPickerTitle(),options:this.pickerOptions(),
        selected:this.selectedPickerId(),manual:this.agentPanel === 'launcher' && !this.launchListReady || this.agentPanel === 'model' && this.launchChoices.acceptsTypedModel,
        back:() => this.closeAgentPanel(),choose:(v:string) => this.chooseAgentOption(v),browse:(path:string) => this.browseAgentFolder(path)})
    } else {
      Column() {
        Row() {
          Button({type:ButtonType.Circle}) { PhoneNavigationIcon({mark:'navigationClose'}) }
            .width(PhoneStyle.bar).height(PhoneStyle.bar).padding(0).backgroundColor(Colors.sheetCard)
            .border({width:0.5,color:Colors.divider,radius:PhoneStyle.bar/2})
            .accessibilityText(tr('cancel')).onClick(() => { this.chatForward.clearNew(); this.showSheet=false; }).enabled(!this.creatingAgent).id('agent.cancel')
          Text(tr('phone_new_chat_title')).fontSize(17).maxFontScale(Tokens.chromeFontMaxScale).maxLines(2).fontWeight(600).fontColor(Colors.ink).textAlign(TextAlign.Center).layoutWeight(1)
          Blank().width(PhoneStyle.bar)
        }.width('100%').padding({left:PhoneStyle.page,right:PhoneStyle.page,top:16,bottom:10})
        Stack({alignContent:Alignment.Bottom}) {
          this.agentStartFields()
          if (this.environment.keyboardHeightPx > 0) {
            Rect().width('100%').height(this.agentActionBottom()).fill(Colors.sheetBackground)
              .hitTestBehavior(HitTestMode.Block).accessibilityLevel('no').id('agent.keyboard.inset')
          }
          this.agentStartFooter()
        }.width('100%').layoutWeight(1).clip(true)
      }.width('100%').height('100%').backgroundColor(Colors.sheetBackground)
    }
  }
  @Builder agentStartFields() {
    Scroll(this.agentFormScroll) {
      Column() {
        if (!this.agentWorktree && !this.agentTodo && this.recentSetups.length) {
          this.agentSectionTitle('flow_recent_setups',false)
          Column() { ForEach(this.recentSetups.slice(0,3),(r:RecentAgentSetup) => {
            Row({space:12}) {
              SymbolGlyph(Symbols.folder).fontSize('17vp').fontColor([Colors.link])
              Column({space:2}) {
                Text() {
                  Span(this.recentFolderTitle(r)).fontWeight(600).fontColor(Colors.ink)
                  Span(' · '+this.recentMachineTitle(r)).fontColor(Colors.secondary)
                }.fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
                Text([r.launcher,r.model,r.preset,r.effort].filter((v:string) => !!v).join(' · ')).fontSize(12).maxFontScale(Tokens.interfaceScaleFor(12)).fontColor(Colors.secondary).maxLines(1).textOverflow({overflow:TextOverflow.Ellipsis})
              }.layoutWeight(1).alignItems(HorizontalAlign.Start)
              if (this.agentMachineKey === r.machine && this.directory === r.cwd && this.launcher === r.launcher && this.agentModelId === r.model && this.agentEffort === r.effort)
                SymbolGlyph(Symbols.check).fontSize('16vp').fontColor([Colors.link])
            }.width('100%').padding({left:16,right:16,top:8,bottom:8}).constraintSize({minHeight:48})
              .onClick(() => this.reuseSetup(r)).accessibilityGroup(true).id('agent.recent')
          },(r:RecentAgentSetup) => r.key) }.width('100%').backgroundColor(Colors.sheetCard).borderRadius(14)
        }
        if (!this.agentWorktree) {
          this.agentSectionTitle('phone_machines',!this.agentTodo && this.recentSetups.length > 0)
          Scroll() { Row({space:12}) {
            ForEach(this.agentMachines(),(m:ResourceRow) => this.machineChoice(m),(m:ResourceRow) => JSON.stringify([m.group,m.manager,m.id,m.title,m.state,this.machineChoiceDetail(m)]))
          } }.scrollable(ScrollDirection.Horizontal).scrollBar(BarState.Off).align(Alignment.Start).width('100%')
            .padding({left:16,right:16,top:12,bottom:12}).backgroundColor(Colors.sheetCard).borderRadius(PhoneStyle.cardRadius)
        }
        if (this.agentWorktree) {
          this.agentSectionTitle('project_join_space',false)
          Column() {
            Row({space:12}) {
              SymbolGlyph(Symbols.spaces).fontSize('18vp').fontColor([Colors.link])
              Text(this.agentWorktree.title).fontSize(17).maxFontScale(Tokens.interfaceScaleFor(17)).fontColor(Colors.ink)
                .layoutWeight(1).id('agent.workspace')
            }.width('100%').padding({left:20,right:20,top:12,bottom:12})
            this.launchDivider()
            Text(this.agentPathLabel(this.agentWorktree.cwd)).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontFamily('monospace')
              .fontColor(Colors.secondary).width('100%').padding({left:20,right:20,top:12,bottom:12}).id('agent.worktree')
          }.width('100%').backgroundColor(Colors.sheetCard).borderRadius(PhoneStyle.cardRadius)
          Text(tr('project_join_help')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
            .width('100%').padding({left:20,right:20,top:6})
        } else if (this.selectedAgentTree() || this.availableAgentTrees().some((t:ProjectWorktree) => t.pickerWorkspace)) {
          Column() {
            this.workspaceOption()
            if (this.selectedAgentTree()) {
              if (this.selectedAgentTree()!.permits('create')) {
                this.launchDivider()
                Button(tr('tree_create'),{buttonStyle:ButtonStyleMode.TEXTUAL}).fontSize(15).maxFontScale(Tokens.interfaceScaleFor(15)).fontColor(Colors.link).onClick(() => this.makeChildFromAgent()).id('agent.child')
              }
            }
          }.width('100%').alignItems(HorizontalAlign.Start).backgroundColor(Colors.sheetCard).borderRadius(PhoneStyle.cardRadius)
            .margin({top:35})
          if (this.selectedAgentTree()) {
            Column() {
              Text(this.selectedTreeKind(this.selectedAgentTree()!)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
                .fontColor(Colors.secondary).id('agent.worktree.kind')
              Text(this.agentPathLabel(this.selectedAgentTree()!.cwd)).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
                .fontColor(Colors.secondary).id('agent.worktree')
            }.width('100%').alignItems(HorizontalAlign.Start).padding({left:20,right:20,top:6})
          }
        }
        if (!this.selectedAgentTree() && !this.agentWorktree) {
          this.agentSectionTitle('directory',true)
          Column() {
            this.agentOptionRow('folder')
            ForEach(this.recentAgentDirectories.slice(0,6),(row:LaunchChoice) => {
              Stack({alignContent:Alignment.TopStart}) {
                this.recentDirectory(row)
                Divider().strokeWidth(Tokens.divider).color(Colors.divider).margin({left:this.agentSize(48,55),right:20})
                  .hitTestBehavior(HitTestMode.None).accessibilityLevel('no')
              }.width('100%')
            },(row:LaunchChoice) => row.id)
          }.width('100%').backgroundColor(Colors.sheetCard).borderRadius(PhoneStyle.cardRadius)
        }
        this.agentSectionTitle('flow_launcher_model_once',true)
        Column() {
          if (this.agentPresets.length) {
            this.agentOptionRow('preset')
            this.launchDivider()
          }
          this.agentOptionRow('launcher')
          if (this.effectiveLauncher()) {
            this.launchDivider()
            this.agentOptionRow('model')
          }
          if (this.launchChoices.startEfforts(this.chosenLauncher()).length > 0) {
            this.launchDivider()
            this.agentOptionRow('effort')
          }
        }.width('100%').backgroundColor(Colors.sheetCard).borderRadius(14)
        Text(this.agentModelHelp()).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13)).fontColor(Colors.secondary)
          .width('100%').padding({left:12,right:12,top:6}).id('agent.model.help')
        this.agentSectionTitle('flow_first_message',true)
        if (this.chatForward.newFor(this.agentModel)) PhoneForwardNewTray({model:this.agentModel,state:this.chatForward})
        TextArea({text:this.newTask,placeholder:tr(this.promptOptional() ? 'flow_draft_optional' : 'task')})
          .keyboardAppearance(this.environment.dark ? KeyboardAppearance.DARK_IMMERSIVE : KeyboardAppearance.LIGHT_IMMERSIVE)
          .minLines(3).maxLines(10,{overflowMode:MaxLinesMode.SCROLL}).width('100%')
          .constraintSize({minHeight:this.agentSize(94.33,115.67)})
          .fontSize(Tokens.interfaceFontVp(17,this.environment.fontScale)).placeholderFont({size:Tokens.interfaceFontVp(17,this.environment.fontScale)})
          .padding({left:16,right:16,top:11,bottom:11}).fontColor(Colors.ink).backgroundColor(Colors.sheetCard).borderRadius(PhoneStyle.cardRadius)
          .onFocus(() => this.focusAgentDraft('agent.task'))
          .onBlur(() => { if (this.focusedAgentField === 'agent.task') this.focusedAgentField = ''; })
          .onChange((v:string) => { this.newTask = v; this.saveAgentDraft(); }).id('agent.task')
        this.agentSectionTitle('flow_start_tab_name',true)
        TextInput({text:this.agentTabName,placeholder:tr('flow_start_tab_name_placeholder')})
          .keyboardAppearance(this.environment.dark ? KeyboardAppearance.DARK_IMMERSIVE : KeyboardAppearance.LIGHT_IMMERSIVE)
          .fontSize(Tokens.interfaceFontVp(17,this.environment.fontScale))
          .placeholderFont({size:Tokens.interfaceFontVp(17,this.environment.fontScale)})
          .fontColor(Colors.ink).backgroundColor(Colors.sheetCard).borderRadius(PhoneStyle.cardRadius)
          .height(this.agentSize(52.33,59.33)).width('100%').padding({left:20,right:20})
          .enterKeyType(EnterKeyType.Done).accessibilityText(tr('flow_start_tab_name'))
          .onFocus(() => this.focusAgentDraft('agent.tab_name'))
          .onBlur(() => { if (this.focusedAgentField === 'agent.tab_name') this.focusedAgentField = ''; })
          .onChange((value:string) => { this.agentTabName = value; }).id('agent.tab_name')
        Text(tr('flow_start_tab_name_help')).fontSize(13).maxFontScale(Tokens.interfaceScaleFor(13))
          .fontColor(Colors.secondary).width('100%').padding({left:20,right:20,top:6}).id('agent.tab_name.help')
        if ((this.agentWorktree || this.pickedAgentTree) && !this.selectedAgentTree()) Text(tr('project_gone')).fontSize(14).maxFontScale(Tokens.interfaceScaleFor(14)).fontColor(Colors.danger).padding(8)
      }.width('100%').alignItems(HorizontalAlign.Start).padding({left:16,right:16,bottom:this.agentScrollClearance()}).id('agent.fields.content')
        .onAreaChange((before:Area,after:Area) => this.agentDraftAreaChanged(before,after))
    }.width('100%').height('100%').align(Alignment.TopStart).id('agent.fields')
      .onAreaChange((before:Area,after:Area) => this.agentDraftAreaChanged(before,after))
      .onWillScroll((_x:number,_y:number,_state:ScrollState,source:ScrollSource):void => this.agentFormWillScroll(source))
      .onScrollStop(() => {
        if (this.focusedAgentField && this.agentFocusScrollOrigin >= 0 && this.environment.keyboardHeightPx > 0)
          this.revealFocusedAgentDraft();
      })
  }
  private async loadAgentLaunchers(): Promise<void> {
    const rev = ++this.launcherRevision, form = this.agentFormRevision, owner = this.agentModel, machine = this.chosenAgentMachine()?.id;
    this.agentLauncherOptions = []; this.agentLauncherLabels = []; this.agentPresets = []; this.agentTierProfile = ''; this.machineDefaultLabel = ''; this.launchListReady = false; this.launcherData = []; this.blockedLaunchers = []; this.defaultLauncherName = ''; this.agentUsesModelSettings = false; this.agentConfiguredModel = '';
    if (!machine || !owner.connected) return;
    const scope = owner.scope(), epoch = owner.connectionGeneration();
    try {
      const replies = await Promise.all([
        owner.repo().call('/api/v1/machines/' + encodeURIComponent(machine) + '/launchers?purpose=agent.new'),
        owner.repo().call('/api/v1/model-config/purposes/agent.new').catch((): Record<string,Object> => Wire.payload([],[]))
      ]);
      const reply = replies[0];
      if (rev !== this.launcherRevision || form !== this.agentFormRevision || !owner.owns(epoch,scope) || this.chosenAgentMachine()?.id !== machine) return;
      const standard = Wire.object(reply,'default');
      const purpose = Wire.object(replies[1],'purpose'), configured = Wire.object(purpose,'effective');
      this.agentUsesModelSettings = Wire.text(purpose,'id') === 'agent.new';
      const configuredProvider = this.agentUsesModelSettings ? Wire.text(configured,'provider') : '';
      this.agentConfiguredModel = configuredProvider ? Wire.text(configured,'model') : '';
      this.defaultLauncherName = (configuredProvider || Wire.text(standard,'provider')).replace(/^launcher\./,'');
      const machineProvider = Wire.text(standard,'provider').replace(/^launcher\./,''), machineModel = Wire.text(standard,'model');
      const label = machineProvider || machineModel ? [machineProvider,machineModel] : [this.defaultLauncherName,this.agentConfiguredModel];
      this.machineDefaultLabel = label.filter((v:string) => !!v).join(' · ');
      const allowed = Wire.rows(reply, 'launchers').filter((item: Record<string,Object>) => !!Wire.text(item,'name') && !Object.keys(Wire.object(item,'block')).length);
      this.launcherData = allowed; this.launchListReady = Array.isArray(reply['launchers']);
      const blocked = Wire.rows(reply,'launchers').filter((v:Record<string,Object>) => !!Object.keys(Wire.object(v,'block')).length)
        .concat(Wire.rows(reply,'unavailable'));
      this.blockedLaunchers = blocked.filter((v:Record<string,Object>) => !!Wire.text(v,'name')).map((v:Record<string,Object>) => {
        const reason = Wire.text(Wire.object(v,'block'),'reason') || Wire.text(v,'reason') || tr('offline');
        return new LaunchChoice(Wire.text(v,'name'),builtinLauncherTitle(Wire.text(v,'name'),Wire.text(v,'display_name')),reason,true);
      });
      this.agentLauncherOptions = allowed.map((item: Record<string,Object>) => Wire.text(item,'name'));
      this.agentTierProfile = Wire.text(Wire.object(reply,'tiers_profile'),'name');
      this.agentPresets = Wire.rows(reply,'tiers').filter((v:Record<string,Object>) => !!Wire.text(v,'name') && this.agentLauncherOptions.includes(Wire.text(v,'launcher')));
      this.agentLauncherLabels = allowed.map((item: Record<string,Object>) => builtinLauncherTitle(Wire.text(item,'name'),Wire.text(item,'display_name')));
      if (!this.agentPanel) this.loadAgentModels();
    } catch (_) { /* Older nodes support entering the existing launcher name directly. */ }
  }
  private agentDraftKey(): string { return 'new-agent:' + (this.agentWorktree?.key || this.agentTodo?.id || 'standalone'); }
  private async prepareAgent(todo?: ResourceRow, tree?: ProjectWorktree, keepForward: boolean = false, atDestination?: () => boolean): Promise<void> {
    if (!keepForward) this.chatForward.clearNew();
    const revision = ++this.agentFormRevision, generation = this.model.connectionGeneration(), scope = this.model.scope();
    const current = (): boolean => revision === this.agentFormRevision && this.model.owns(generation,scope) && (!atDestination || atDestination());
    this.focusedAgentField = ''; this.agentTabName = '';
    this.startTreeSource = undefined; this.startWorktree.close(); this.startFailure = ''; this.agentPanel = '';
    this.launchChoices.clear(); this.recentSetups = []; this.recentAgentDirectories = []; this.agentModelId = ''; this.agentEffort = '';
    this.machineIntent++; this.switchingAgentMachine = false; this.agentMachineKey = ''; this.agentPreset = ''; this.projectCatalog.startError = false; this.readerShown = false; this.creatingAgent = false; this.newTask = ''; this.directory = ''; this.launcher = ''; this.agentTodo = todo; this.agentWorktree = undefined; this.pickedAgentTree = '';
    try {
      let owner = todo ? await this.model.viewFor(todo) : this.model;
      if (tree) {
        const machine = this.model.directoryMachines.find((row: ResourceRow) => row.group === tree.group && row.manager === tree.manager && row.id === tree.machine);
        if (!tree.canStart || !machine || !this.agentMachineOnline(machine)) throw new Error('request_updated');
        owner = await this.model.viewFor(machine);
      }
      if (!current()) return;
      this.agentModel = owner; this.agentWorktree = tree;
      if (tree) {
        const index = this.agentMachines().findIndex((row: ResourceRow) => row.id === tree.machine);
        if (index < 0 || !owner.connected) throw new Error('request_updated');
        this.agentMachineKey = this.machineKey(this.agentMachines()[index]);
      }
      if (!tree && !this.projectCatalog.projects.length && !this.projectCatalog.loading) this.projectCatalog.refresh(this.model);
      const raw = await owner.loadView(this.agentDraftKey());
      if (!current()) return;
      const saved = Wire.parse(raw); this.newTask = Wire.text(saved, 'task', todo ? todo.title + (Wire.text(todo.data(), 'notes') ? '\n\n' + Wire.text(todo.data(), 'notes') : '') : '');
      if (!tree) {
        const index = this.agentMachines().findIndex((row: ResourceRow) => row.id === Wire.text(saved, 'machine') && (!row.group || row.group === owner.profile.id) && (!row.manager || row.manager === owner.profile.manager));
        if (index >= 0) this.agentMachineKey = this.machineKey(this.agentMachines()[index]);
        this.pickedAgentTree = Wire.text(saved, 'tree');
      }
      this.directory = tree ? tree.cwd : Wire.text(saved, 'directory'); this.launcher = Wire.text(saved, 'launcher'); this.agentPreset = Wire.text(saved,'preset');
      this.agentModelId = Wire.text(saved,'model'); this.agentEffort = Wire.text(saved,'effort');
      if (!this.agentMachineKey) {
        const local = this.agentMachines().filter((m:ResourceRow) => (!m.group || m.group === owner.profile.id) && (!m.manager || m.manager === owner.profile.manager));
        const m = local.find((v:ResourceRow) => v.id === Wire.text(saved,'machine')) || local.find((v:ResourceRow) => this.agentMachineOnline(v));
        if (m) this.agentMachineKey = this.machineKey(m);
      }
      this.sheetKind = 'newagent'; this.showSheet = true;
      const first = this.chosenAgentMachine();
      if (first && first.group && (first.group !== owner.profile.id || first.manager !== owner.profile.manager)) this.pickAgentMachine(first,true);
      else { this.loadAgentLaunchers(); this.loadRecentSetups(); }
    } catch (error) { if (current()) {
        if (tree) this.projectCatalog.startError = true;
        this.model.reportError(error as Error);
      } }
  }
  private saveAgentDraft(): void {
    if (this.sheetKind !== 'newagent' || !this.showSheet) return;
    const owner = this.agentModel, generation = owner.connectionGeneration(), scope = owner.scope();
    owner.saveView(this.agentDraftKey(), JSON.stringify(Wire.payload(['task', 'directory', 'launcher', 'machine', 'tree', 'preset','model','effort'], [this.newTask, this.directory, this.launcher, this.chosenAgentMachine()?.id || '', this.pickedAgentTree,this.agentPreset,this.agentModelId,this.agentEffort])))
      .catch(() => { if (owner.owns(generation, scope)) owner.error = 'cache_unavailable'; });
  }
  private machineKey(row:ResourceRow): string {
    return JSON.stringify([row.group || this.agentModel.profile.id,row.manager || this.agentModel.profile.manager,row.id]);
  }
  private sameMachine(a:ResourceRow,b:ResourceRow|undefined):boolean { return !!b && this.machineKey(a) === this.machineKey(b); }
  private agentOwnsMachine(row:ResourceRow|undefined):boolean {
    return !!row && (!row.group || row.group === this.agentModel.profile.id) && (!row.manager || row.manager === this.agentModel.profile.manager);
  }
  private agentMachineOnline(row:ResourceRow):boolean {
    const data = row.data();
    return row.state !== 'offline' && Wire.bool(data,'online',true) && Wire.text(data,'trust') !== 'pending' && !Wire.bool(data,'pending_confirm') && !Wire.bool(data,'pending');
  }
  private agentMachines():ResourceRow[] {
    const owner = this.agentModel;
    if (this.agentWorktree || this.agentTodo) return owner.machines.filter((v:ResourceRow) => !v.manager || v.manager === owner.profile.manager);
    return this.model.directoryMachines.length ? this.model.directoryMachines : owner.machines;
  }
  private async pickAgentMachine(row:ResourceRow,force:boolean = false):Promise<void> {
    if (!this.agentMachineOnline(row) || !force && this.sameMachine(row,this.chosenAgentMachine()) && this.agentOwnsMachine(row) && this.agentModel.connected || this.agentWorktree && row.id !== this.agentWorktree.machine) return;
    const intent = ++this.machineIntent, form = this.agentFormRevision, root = this.model, epoch = root.connectionGeneration(), scope = root.scope();
    this.agentMachineKey = this.machineKey(row); this.switchingAgentMachine = true; this.startFailure = ''; this.launcherRevision++;
    this.launchChoices.clear(); this.recentSetups = []; this.recentAgentDirectories = []; this.pickedAgentTree = ''; this.directory = ''; this.launcher = ''; this.agentPreset = ''; this.agentModelId = ''; this.agentEffort = '';
    try {
      const owner = await root.viewFor(row);
      if (intent !== this.machineIntent || form !== this.agentFormRevision || !root.owns(epoch,scope)) return;
      if (!owner.connected) throw new Error('offline');
      this.agentModel = owner; this.saveAgentDraft(); this.loadAgentLaunchers(); this.loadRecentSetups();
    } catch (error) { if (intent === this.machineIntent && form === this.agentFormRevision) this.startFailure = (error as Error).message || 'offline'; }
    finally { if (intent === this.machineIntent && form === this.agentFormRevision) this.switchingAgentMachine = false; }
  }
  private async loadInitialAgentDirectory(): Promise<void> {
    const machine = this.chosenAgentMachine(), owner = this.agentModel, revision = this.agentFormRevision, intent = this.machineIntent;
    if (!machine || this.agentWorktree || !this.agentOwnsMachine(machine)) return;
    const epoch = owner.connectionGeneration(), scope = owner.scope();
    try {
      const result = await owner.repo().call('/api/v1/machines/'+encodeURIComponent(machine.id)+'/dirs?path=');
      if (owner !== this.agentModel || !owner.owns(epoch,scope) || this.agentFormRevision !== revision || this.machineIntent !== intent ||
        !this.showSheet || this.sheetKind !== 'newagent' || !this.sameMachine(machine,this.chosenAgentMachine())) return;
      this.recentAgentDirectories = recentLaunchDirectories(result);
      if (!this.directory && !this.pickedAgentTree && !this.agentPanel) {
        this.directory = this.recentAgentDirectories[0]?.id || Wire.text(result,'path');
      }
    } catch (_) { /* The folder picker can retry an unavailable directory. */ }
  }
  private async loadRecentSetups():Promise<void> {
    this.loadInitialAgentDirectory();
    const owner = this.agentModel, form = this.agentFormRevision, epoch = owner.connectionGeneration(), scope = owner.scope();
    try {
      const list = loadAgentSetups(await owner.loadView('recent-agent-setups'));
      if (form === this.agentFormRevision && owner === this.agentModel && owner.owns(epoch,scope)) this.recentSetups = list;
    } catch (_) { /* Recent entries are optional. */ }
  }
  private recentFolderTitle(setup:RecentAgentSetup):string {
    const machine = this.agentMachines().find((row:ResourceRow) => this.machineKey(row) === setup.machine);
    const cwd = setup.cwd.replace(/\/+$/,'') || setup.cwd;
    if (machine && cwd === Wire.text(machine.data(),'home') && cwd) return '~';
    return cwd.split('/').filter((part:string) => !!part).pop() || cwd || tr('project_temporary_session');
  }
  private recentMachineTitle(setup:RecentAgentSetup):string {
    return this.agentMachines().find((m:ResourceRow) => this.machineKey(m) === setup.machine)?.title || tr('machines');
  }
  private async reuseSetup(setup:RecentAgentSetup):Promise<void> {
    const row = this.agentMachines().find((m:ResourceRow) => this.machineKey(m) === setup.machine);
    if (!row || !this.agentMachineOnline(row)) { this.startFailure = 'offline'; return; }
    await this.pickAgentMachine(row);
    if (!this.sameMachine(row,this.chosenAgentMachine()) || this.startFailure || !this.showSheet) return;
    this.pickedAgentTree = setup.tree; this.directory = setup.cwd; this.launcher = setup.launcher;
    this.agentModelId = setup.model; this.agentEffort = setup.effort; this.agentPreset = setup.preset; this.saveAgentDraft();
  }
  private availableAgentTrees(): ProjectWorktree[] {
    const machine = this.chosenAgentMachine(), owner = this.agentModel, rows: ProjectWorktree[] = [];
    if (!machine || !this.agentOwnsMachine(machine)) return rows;
    for (const project of this.projectCatalog.projects) {
      for (const tree of project.trees) {
        if (tree.canStart && tree.machine === machine.id && tree.group === owner.profile.id && tree.manager === owner.profile.manager) rows.push(tree);
      }
    }
    return rows;
  }
  private selectedAgentTree():ProjectWorktree|undefined {
    if (this.pickedAgentTree) return this.availableAgentTrees().find((tree:ProjectWorktree) => tree.key === this.pickedAgentTree);
    if (!this.agentWorktree) return undefined;
    const preset = this.agentWorktree;
    if (preset.receiptCurrent && !preset.receiptCurrent()) return undefined;
    const current = preset.receiptCurrent ? this.projectCatalog.selectedWorktree(preset.key) : this.projectCatalog.tree(preset.key);
    return current && current.cwd === preset.cwd && (!preset.receiptCurrent || current.matchesCreated(preset)) ? current : undefined;
  }
  private chosenAgentMachine():ResourceRow|undefined {
    const machines = this.agentMachines();
    if (this.agentMachineKey) return machines.find((row:ResourceRow) => this.machineKey(row) === this.agentMachineKey);
    if (this.agentWorktree) return machines.find((row:ResourceRow) => row.id === this.agentWorktree?.machine);
    return machines.find((row:ResourceRow) => this.agentMachineOnline(row));
  }
  private agentFingerprint(): string { return JSON.stringify([this.agentModel.scope(), this.agentTodo?.id || '', this.chosenAgentMachine()?.id || '', this.newTask, this.agentTabName, this.directory, this.launcher, this.agentModelId, this.agentEffort, this.agentWorktree?.key || this.pickedAgentTree, this.selectedAgentTree()?.key || '', this.selectedAgentTree()?.cwd || '']); }
  private async createAgent(): Promise<void> {
    const machine = this.chosenAgentMachine(), tree = this.selectedAgentTree(); if (!machine || !this.canStartAgent()) return;
    const owner = this.agentModel, generation = owner.connectionGeneration(), scope = owner.scope(), rootGeneration = this.model.connectionGeneration(), rootScope = this.model.scope(), revision = this.agentFormRevision, fingerprint = this.agentFingerprint(), draftKey = this.agentDraftKey();
    const valid = (): boolean => owner.owns(generation, scope) && this.model.owns(rootGeneration, rootScope) && revision === this.agentFormRevision && this.showSheet && this.sheetKind === 'newagent' && fingerprint === this.agentFingerprint() &&
      (!this.agentWorktree?.receiptCurrent || this.selectedAgentTree()?.canStart === true && !!this.chosenAgentMachine() && this.agentMachineOnline(this.chosenAgentMachine()!));
    this.creatingAgent = true;
    try {
      const effort = this.startAgentEffort();
      const note=this.newTask,prompt=this.chatForward.newPrompt(owner,note);
      const row = await owner.createAgent(prompt, machine, this.directory, this.launcher, this.agentTodo?.id || '', valid, tree?.boundStart ? tree.sourceId : '', tree?.boundStart ? tree.source : '', this.agentModelId, effort, this.agentTabName);
      if (!valid()) return;
      await this.chatForward.afterNew(owner,row,note);
      if (!valid()) return;
      const setup = new RecentAgentSetup(); setup.machine = this.machineKey(machine); setup.cwd = this.directory; setup.tree = tree?.key || '';
      setup.launcher = this.launcher; setup.model = this.agentModelId; setup.effort = effort; setup.preset = this.agentPreset;
      const updated = rememberAgentSetup(this.recentSetups,setup);
      owner.saveView('recent-agent-setups',JSON.stringify(Wire.payload(['entries'],[updated]))).catch(() => {});
      this.showSheet = false; this.newTask = ''; owner.saveView(draftKey, '').catch(() => {});
      row.group = owner.profile.id; row.manager = owner.profile.manager;
      if (this.projectPlace() || this.agentTodo) this.openLinkedResource(row); else this.openPickedChat(row);
    } catch (error) { if (valid()) this.startFailure = (error as Error).message || 'failed'; }
    finally { if (revision === this.agentFormRevision) this.creatingAgent = false; }
  }
  private routeRow(param: Object): ResourceRow { return NavigationContext.rowFrom(param as string); }
  private chatTitle(param: Object): string {
    const row = this.routeRow(param);
    const current = this.detailModel.sessions.concat(this.detailModel.conversations).find((item: ResourceRow) => item.key() === row.key());
    const target = current || row;
    if (this.detailModel.isAutoRenamingRow(target)) {
      return tr('agent_renaming');
    }
    return sessionDisplayTitle(target.title, target.data(), this.detailModel.useHerdrTabName) || tr(row.kind === 'conversation' ? 'main_chat' : 'agents');
  }
  private async restoreTarget(name: string, param: Object): Promise<void> {
    const descriptor = NavigationContext.from(name, param as string);
    if (!descriptor.scoped) return;
    const key = this.childKey(name, param), revision = ++this.routeRevision, generation = this.model.connectionGeneration(), scope = this.model.scope();
    const keepContent = this.targetReady(name, param);
    if (!keepContent) { this.chatReady = false; this.detailRoute = ''; }
    try {
      let row = descriptor.row; const view = await this.model.viewFor(row);
      if (['session', 'conversation'].includes(row.kind) && !row.machine) {
        let resolved = view.conversations.concat(view.sessions).find((item: ResourceRow) => item.id === row.id || item.session === row.session && !!row.session);
        if (!resolved && view.connected) { await view.refresh(); resolved = view.conversations.concat(view.sessions).find((item: ResourceRow) => item.id === row.id || item.session === row.session && !!row.session); }
        if (!resolved) throw new Error('request_updated'); row = resolved;
      }
      if (revision !== this.routeRevision || this.activeChild !== key || !this.model.owns(generation, scope)) return;
      if (this.detailModel !== view) this.detailModel.hideConversation();
      this.detailModel = view;
      if (row.key() !== view.selected.key()) await view.select(row);
      else if (name === 'session' || name === 'conversation') { if (view.connected) view.showConversation(row); }
      if (revision !== this.routeRevision || this.activeChild !== key || !this.model.owns(generation, scope) || view.selected.key() !== row.key()) return;
      this.markDetailReady(key);
      if (name === 'assistant' || name === 'settings' && descriptor.page === 'voice') view.watchDecisions(true);
    } catch (error) { if (revision === this.routeRevision && this.activeChild === key && this.model.owns(generation, scope)) this.model.reportError(error as Error); }
  }
  private routeShown(name: string, param: Object): void {
    const last = this.stack.size()-1;
    if (last < 0 || this.stack.getAllPathName()[last] !== name || this.stack.getParamByIndex(last) !== param) return;
    this.syncPhonePath();
    if (this.activeChild !== this.childKey(name, param)) this.dismissChatActions();
    this.activeChild = this.childKey(name, param); if (name === 'extension') this.extensionTitle = this.routeRow(param).title; if (name === 'todo') this.todoTitle = this.routeRow(param).title; this.shownName = name; this.shownParam = param as string;
    this.detailModel.watchDecisions(false);
    if (!['session', 'conversation'].includes(name)) this.detailModel.hideConversation(name !== 'tools');
    if (name === 'applications') this.applications.refresh();
    if (['projects','project','project_tree','machine_projects'].includes(name) && !this.projectCatalog.projects.length && !this.projectCatalog.loading) this.projectCatalog.refresh(this.model);
    if (name === 'settings' && this.settingsKind(param) === 'appearance') this.ensureAgentWorkspace();
    const descriptor = NavigationContext.from(name, param as string);
    if (descriptor.scoped) this.restoreTarget(name, param);
    else { this.routeRevision++; this.markDetailReady(this.activeChild); if (name === 'resources') this.model.listResources(param as string); }
  }
  @Builder targetLoading(name: string, param: Object) {
    Column({ space: Tokens.gap }) {
      if (this.model.error) {
        Text(trError(this.model.error)).fontSize(Tokens.meta)
        Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry')).bindTips(tr('retry')).onClick(() => this.restoreTarget(name, param))
      } else LoadingProgress().width(Tokens.touch).height(Tokens.touch)
    }.width('100%').height('100%').justifyContent(FlexAlign.Center)
  }
  @Builder destinationTitle(name: string, param: Object) {
    if (this.mainDestination(name, param) && !this.wide()) this.peerTitle(true)
    else Text(this.destinationTitleText(name, param)).height(this.headerHeight()).fontSize(Tokens.title).fontWeight(FontWeight.Medium).maxLines(1).textOverflow({ overflow: TextOverflow.Ellipsis })
      .padding({ left: this.destinationBackHidden(name, param) ? Tokens.page : 0 })
  }
  private destinationBackHidden(name: string, param: Object): boolean {
    if (this.mainDestination(name, param) || name === 'session' && Wire.bool(this.routeRow(param).data(), 'navigation_root') && this.wide() && this.model.section === 1) return true;
    return this.wide() && this.stack.size() === 1 && this.stack.getAllPathName()[0] === name && this.stack.getParamByIndex(0) === param;
  }
  private destinationTitleText(name: string, param: Object): string {
    if (name === 'resources' && param === 'health' && !this.wide()) return tr('phone_drawer_health');
    if (name === 'machine_agents') return this.routeRow(param).title || tr('phone_machines');
    if (name === 'machine_settings') return tr('phone_machine_detail');
    if (name === 'settings' && this.settingsKind(param) === 'machines') return tr('phone_machines');
    if (name === 'recordings') return tr('knowledge_kind_meetings');
    if (name === 'knowledge_meetings') return tr('knowledge');
    if (name === 'knowledge_map') return tr('ks_map_title');
    if (name === 'knowledge' && this.routeRow(param).path === 'recordings') return tr('knowledge_kind_recordings');
    if (name === 'projects') return tr('projects');
    if (name === 'machine_projects') return this.projectCatalog.projects.find((row: PersonaProject) => row.hostKey === param)?.machine || tr('phone_machines');
    if (name === 'project') return this.projectCatalog.projects.find((row: PersonaProject) => row.key === param)?.title || tr('projects');
    if (name === 'project_tree') return this.projectCatalog.tree(param as string)?.title || tr('project_worktree');
    if (name === 'settings_root') return tr('settings');
    if (name === 'assistant') return tr(Wire.text(this.routeRow(param).data(), 'assistant_item_kind') === 'plan' ? 'assistant_feed_plan' : 'assistant_feed_receipt');
    return name === 'session' || name === 'conversation' ? this.chatTitle(param) : name === 'resources' ? tr(param === 'todo' ? 'todos' : param === 'inbox' ? 'inbox' : param === 'health' ? 'health_home_title' : 'knowledge') : name === 'applications' ? tr('applications') : name === 'extension' ? this.extensionTitle : name === 'todo' ? this.todoTitle || this.routeRow(param).title || this.routeRow(param).id : name === 'settings' ? this.settingsTitle(param) : name === 'tools' ? tr('workspace') : name === 'history' ? tr('interaction_history') : this.routeRow(param).title || this.routeRow(param).id;
  }
  @Builder destination(name: string, param: Object) {
    NavDestination() {
      if (['projects','project','project_tree','machine_projects'].includes(name)) {
        this.projectPage(name, param as string)
      } else if (name === 'session' || name === 'conversation') {
        if (this.targetReady(name, param)) Conversation({ readingHeader:this.agentReadingHeader,headerOwner:this.activeChild===this.childKey(name,param) ? this.readingHeaderOwner : '', onOpenTriage: () => { if (this.activeChild === this.childKey(name, param)) this.openSettings('triage', true); }, liftHost:this.messageLift, forwardState:this.chatForward, onForwardOrigin:(row:ResourceRow,item:string):void => this.openPickedChat(row,item), searchPresented:this.chatSearchShown, searchHitId:this.searchHitFor(param), decisionTool:this.decisionToolFor(param), model: this.detailModel, onPlace: (kind: string) => { if (this.activeChild === this.childKey(name, param)) this.openSpace(kind); }, admission: (): boolean => this.activeChild === this.childKey(name, param) && this.targetReady(name, param), onOpenFile: (path: string) => this.openTools(path), active: this.activeChild === this.childKey(name, param), onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address), showContextMenu: this.mainDestination(name, param) || this.hideChatTitle(name, param), onOpenCard: (itemId: string, kind: string) => { if (this.activeChild === this.childKey(name, param) && this.targetReady(name, param)) this.openAssistantCard(itemId, kind); }, onLongDraftHeader: (floating: boolean) => {
          const child = this.childKey(name, param);
          if (floating && this.activeChild === child && this.targetReady(name, param)) this.longDraftIdentity = child;
          else if (!floating && this.longDraftIdentity === child) this.longDraftIdentity = '';
        }, onPrimaryStop: (shown: boolean) => { if (this.activeChild === this.childKey(name, param) && this.targetReady(name, param)) this.inlineStopIdentity = shown ? JSON.stringify([this.detailModel.scope(), this.detailModel.connectionGeneration(), this.detailModel.target()]) : ''; }, onShowActions: () => this.showChatActions(name, param), actionItems: this.chatMenuActions, actionsVisible: this.chatBodyMenuVisible && this.activeChild === this.childKey(name, param), actionsRequest: this.chatMenuRevision, onHideActions: (request: number) => this.closeChatActions(request) })
        else this.targetLoading(name, param)
      } else if (name === 'history') {
        if (this.targetReady(name, param)) InteractionHistoryPage({ model: this.detailModel, active: this.activeChild === this.childKey(name, param), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'tools') {
        if (this.targetReady(name, param)) SessionTools({ model: this.detailModel, phoneSheetHeight:this.compactFormHeight(), initialTab:Wire.number(Wire.parse(param as string),'entry_tab'), onOpenLink: (address: string) => this.openReader(address), initialFile: Wire.text(Wire.parse(param as string), 'open_file'), onWorkspaceReady: (workspace: SessionWorkspace | undefined) => this.detailModel.setWorkspace(workspace) })
        else this.targetLoading(name, param)
      } else if (name === 'applications') {
        ExtensionNavigationPage({ directory: this.applications, onOpen: (entry: ExtensionNavigationEntry): void => { if (this.activeChild === this.childKey(name, param)) this.openApplication(entry); } })
      } else if (name === 'extension') {
        Column() {
          if (!this.applicationAllowed(param)) Row({ space: Tokens.gap }) {
            Text(tr('application_unavailable')).fontSize(Tokens.meta).layoutWeight(1)
            Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry')).bindTips(tr('retry')).enabled(!this.applications.loading).onClick(() => this.applications.refresh())
          }.width('100%').padding(Tokens.page)
          Column() {
        if (this.targetReady(name, param)) ExtensionPage({ model: this.detailModel, extensionId: this.routeRow(param).id, uiId: Wire.text(this.routeRow(param).data(), 'ui'), state: Wire.text(this.routeRow(param).data(), 'state'), context: Wire.object(this.routeRow(param).data(), 'context'), active: this.activeChild === this.childKey(name, param), authorized: this.applicationAllowed(param), refreshRevision: this.extensionRefreshRevision + (Wire.text(this.routeRow(param).data(), 'navigation_extension') ? this.applications.revision : 0), onTitle: (title: string) => { if (this.activeChild === this.childKey(name, param)) this.extensionTitle = title; }, onNavigate: (effect: ExtensionUINavigationEffect) => this.extensionNavigate(name, param, effect), onLink: (address: string) => { if (this.activeChild === this.childKey(name, param)) this.openReader(address); }, onBack: () => { if (this.activeChild === this.childKey(name, param)) { this.stack.pop(); this.savePath(); } } })
            else this.targetLoading(name, param)
          }.width('100%').layoutWeight(1).visibility(this.applicationAllowed(param) ? Visibility.Visible : Visibility.None)
        }.width('100%').height('100%')
      } else if (name === 'machine_agents' || name === 'settings' && this.settingsKind(param) === 'machines') {
        MachineDirectory({model:this.model,settingsScope:this.model.section === 3 || this.phoneSettingsDepth >= 0, selected:name === 'machine_agents' ? this.routeRow(param) : undefined,
          active:this.activeChild === this.childKey(name,param),
          onMachine:(row:ResourceRow) => this.pushPlace('machine_agents',JSON.stringify(row)),
          onDetails:(row:ResourceRow) => this.pushPlace('machine_settings',JSON.stringify(row)),
          onManage:() => this.pushPlace('machine_settings',''),
          onSession:(row:ResourceRow) => this.openFrom(name,param,row)})
      } else if (name === 'machine_settings') {
        if (!this.routeRow(param).id || this.targetReady(name,param)) SettingsPage({model:this.routeRow(param).id ? this.detailModel : this.model, initialMachine:this.routeRow(param).id ? this.routeRow(param) : undefined,
          page:'machines',active:this.activeChild === this.childKey(name,param),onComputer:(row:ResourceRow) => this.openComputer(row),
          onChat:(row:ResourceRow) => this.openFrom(name,param,row)})
        else this.targetLoading(name,param)
      } else if (name === 'computer') {
        if (this.targetReady(name, param)) ComputerScreenPage({ model: this.detailModel, row: this.routeRow(param), active: this.activeChild === this.childKey(name, param), onClose: () => { if (this.activeChild === this.childKey(name, param)) { this.stack.pop(); this.savePath(); } } })
        else this.targetLoading(name, param)
      } else if (name === 'file') {
        if (this.targetReady(name, param)) RemoteDocumentPage({ model: this.detailModel, row: this.routeRow(param), active: this.activeChild === this.childKey(name, param), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'inbox' || name === 'todo_source' || name === 'todo_thread' || name === 'resources' && param === 'inbox') {
        if (name === 'resources' || this.targetReady(name, param)) InboxWorkspacePage({ model: name === 'resources' ? this.model : this.detailModel, row: name === 'resources' ? undefined : this.routeRow(param), list: name === 'resources', phoneList: !this.wide() && name === 'resources', visible: this.activeChild === this.childKey(name, param), refreshRevision: this.inboxRefreshRevision, toolbarActions:!this.wide() && this.rootModuleMenuKind()==='inbox', composeRevision:this.inboxComposeRevision, onComposeAvailability:(enabled:boolean) => { if (this.activeChild===this.childKey(name,param) && this.rootModuleMenuKind()==='inbox') { this.inboxComposeScope=this.inboxPermissionOwner();this.inboxComposeAvailable=enabled; } }, handoffRequested: this.pendingInboxHandoff === this.childKey(name, param), onHandoffConsumed: () => { if (this.pendingInboxHandoff === this.childKey(name, param)) this.pendingInboxHandoff = ''; },
          onChanged: () => { if (this.activeChild === this.childKey(name, param)) this.inboxRefreshRevision++; }, onOpen: (row: ResourceRow) => this.openFrom(name, param, row), onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onHandoff: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'todo' || name === 'resources' && param === 'todo') {
        if (name === 'resources' || this.targetReady(name, param)) TodoWorkspacePage({ model: name === 'resources' ? this.model : this.detailModel, row: name === 'todo' ? this.routeRow(param) : undefined, list: name === 'resources', visible: this.activeChild === this.childKey(name, param),
          phoneList:!this.wide() && name==='resources' && param==='todo', phoneDetail:!this.wide() && name==='todo', createRevision:this.todoCreateRevision,
          onTitle: (title: string) => { if (name === 'todo' && this.activeChild === this.childKey(name, param)) this.todoTitle = title; }, onOpen: (row: ResourceRow) => this.openFrom(name, param, row), onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address), onCreateAgent: (todo: ResourceRow) => this.createAgentFrom(name, param, todo) })
        else this.targetLoading(name, param)
      } else if (name === 'knowledge_map') {
        if (param === this.model.scope()) PhoneKnowledgeMap({ model:this.model,
          onEntry:(row:ResourceRow)=>this.openFrom(name,param,row), onSelection:()=>{ if (this.activeChild===this.childKey(name,param)) this.pushPlace('settings','phone_knowledge_selection'); } })
      } else if (name === 'knowledge' || name === 'recordings' || name === 'knowledge_meetings' || name === 'resources' && param === 'knowledge') {
        if (name !== 'knowledge' || this.targetReady(name, param)) KnowledgeWorkspacePage({ meetingMenu:name==='recordings' ? this.meetingOptions : undefined,landingMenu:!this.wide() && (name==='knowledge_meetings' || name==='resources' && param==='knowledge') ? this.knowledgeMenu : undefined, initialKind: name === 'recordings' ? 'recordings' : name === 'knowledge_meetings' ? 'meetings' : '', editRevision:this.recordingEditRevision, phoneRecordingDetail:name==='knowledge' && !this.wide(), onOpenMap:() => { if (this.activeChild === this.childKey(name,param)) this.pushPlace('knowledge_map',this.model.scope()); }, onAllMeetings:() => { if (this.activeChild === this.childKey(name,param)) this.pushPlace('knowledge_meetings',''); }, model: name !== 'knowledge' ? this.model : this.detailModel, row: name === 'knowledge' ? this.routeRow(param) : undefined, visible: this.activeChild === this.childKey(name, param),
          onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onChat: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'resources' && param === 'health') {
        HealthWorkspacePage({ model: this.model, onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onChat: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address) })
      } else if (name === 'resources' || name === 'resource') {
        if (name === 'resources' || this.targetReady(name, param)) ResourcePage({ model: name === 'resources' ? this.model : this.detailModel, onLink: (address: string) => this.openReader(address), kind: name === 'resources' ? param as string : this.routeRow(param).kind, list: name === 'resources', onOpen: (row: ResourceRow) => this.openFrom(name, param, row),
          onHandoff: (row: ResourceRow) => this.handoffResource(row) })
        else this.targetLoading(name, param)
      } else if (name === 'settings_root') {
        this.settingsList()
      } else if (name === 'assistant') {
        if (this.targetReady(name, param)) AssistantDetailPage({ onOpenTriage: () => { if (this.activeChild === this.childKey(name, param)) this.openSettings('triage', true); }, model: this.detailModel, active: this.activeChild === this.childKey(name, param), itemId: Wire.text(this.routeRow(param).data(), 'assistant_item_id'), kind: Wire.text(this.routeRow(param).data(), 'assistant_item_kind'), onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'settings' && (this.settingsKind(param) === 'phone' || this.settingsKind(param) === 'voice')) {
        if (!NavigationContext.from(name, param as string).scoped || this.targetReady(name, param)) DevicePage({ model: this.settingsModel(param), page: this.settingsKind(param), active: this.activeChild === this.childKey(name, param), onLeave: (owner: ClientModel) => this.restoreDeviceOwner(owner), onLink: (address: string) => this.openReader(address) })
        else this.targetLoading(name, param)
      } else if (name === 'settings' && this.settingsKind(param) === 'phone_knowledge_selection') {
        PhoneKnowledgeSelection({model:this.settingsModel(param)})
      } else if (name === 'settings' && this.settingsKind(param) === 'phone_meeting_options') {
        PhoneMeetingSettings({model:this.settingsModel(param)})
      } else if (name==='settings' && this.settingsKind(param)==='phone_settings_advanced') {
        SettingsDirectory({advanced:true,model:this.settingsModel(param),label:(key:string):string=>this.settingsLabel(key),onOpen:(key:string)=>this.openSettings(key)})
      } else if (name==='settings' && this.settingsKind(param)==='phone_inbox_sources') {
        PhoneInboxSources({model:this.settingsModel(param),navigation:this.inboxSourcesNav,onAsk:(text:string)=>this.askFromInboxSources(text),
          onWeb:(address:string)=>this.openReader(address)})
      } else if (name==='settings' && this.settingsKind(param)==='phone_default_permissions') {
        List() { PhoneDefaultPermissionSection({model:this.settingsModel(param)}) }.width('100%').height('100%').padding(16)
      } else if (name==='settings' && this.settingsKind(param)==='phone_native_alarms') {
        PhoneAlarmsSettings({active:this.activeChild===this.childKey(name,param)})
      } else if (name==='settings' && this.settingsKind(param)==='phone_xiaoyi') {
        XiaoYiSettingsPage({model:this.settingsModel(param)})
      } else if (name==='settings' && ['phone_hubs','phone_pairing','about','storage','phone_live_screen','phone_health_reports'].includes(this.settingsKind(param))) {
        PhoneSettingsDevicePages({model:this.settingsModel(param),page:this.settingsKind(param),onOpen:(key:string)=>this.openSettings(key),onPair:()=>this.pairFromSettings()})
      } else if (name === 'settings' && ['phone_agent_options','phone_language_options'].includes(this.settingsKind(param))) {
        PhoneSettingsOptions({model:this.model,page:this.settingsKind(param),label:(k:string):string => this.settingsLabel(k),onOpen:(k:string) => this.openSettings(k)})
      } else if (name === 'settings' && ['phone_agent_folders', 'phone_claude_config', 'phone_codex_config', 'phone_agy_config', 'phone_agent_followup'].includes(this.settingsKind(param))) {
        PhoneAgentSettingsPage({model:this.settingsModel(param), page:this.settingsKind(param)})
      } else if (name === 'settings' && this.isAdminPage(this.settingsKind(param))) {
        if (!NavigationContext.from(name, param as string).scoped || this.targetReady(name, param)) AdminWorkspacePage({ model: this.settingsModel(param), page: this.settingsKind(param) === 'phone_new_assistant' ? 'managers' : this.settingsKind(param), createManagerOnly:this.settingsKind(param) === 'phone_new_assistant', installingManager:NavigationContext.installingManagerFor(param as string,this.model.scope()), row: NavigationContext.from(name, param as string).scoped ? NavigationContext.from(name, param as string).row : undefined, active: this.activeChild === this.childKey(name, param), refreshRevision: this.adminRefreshRevision,
          onOpenResource: (row: ResourceRow) => this.openFrom(name, param, row), onOpenMachine: (row: ResourceRow) => { if (this.activeChild === this.childKey(name, param)) this.openSettingsDetail('machines', row); } })
        else this.targetLoading(name, param)
      } else if (name === 'settings' && this.settingsKind(param) === 'ssh') {
        SshSettingsPage({ tunnel: this.settingsModel(param).ssh, active: this.activeChild === this.childKey(name, param),
          onSaved: (): void => { const owner = this.settingsModel(param); if (this.activeChild === this.childKey(name, param)) owner.selectProfile(owner.profile, true); } })
      } else if (name === 'settings') {
        if (!NavigationContext.from(name, param as string).scoped || this.targetReady(name, param)) SettingsPage({ model: phoneAppearancePage(this.settingsKind(param)) || this.settingsKind(param) === 'phone_agent_layout' ? this.model : this.settingsModel(param), phoneAppearanceRoute:this.settingsKind(param).startsWith('phone_look_') ? this.settingsKind(param) : '', agentLayoutOnly:this.settingsKind(param) === 'phone_agent_layout', agentWorkspace: this.agentWorkspace, onChat: (row: ResourceRow) => { if (this.activeChild === this.childKey(name, param)) this.openLinkedResource(row); }, active: this.activeChild === this.childKey(name, param), page: this.settingsKind(param) === 'phone_agent_layout' || phoneAppearancePage(this.settingsKind(param)) ? 'appearance' : this.settingsKind(param), onDetail: (page: string, row: ResourceRow) => { if (this.activeChild === this.childKey(name, param)) this.openSettingsDetail(page, row); }, onExtension: (extensionId: string, uiId: string, title: string) => { if (this.activeChild === this.childKey(name, param)) this.openExtension(this.settingsModel(param), extensionId, uiId, title); }, onBack: () => { if (this.activeChild === this.childKey(name, param)) { this.stack.pop(); this.savePath(); } }, onComputer: (row: ResourceRow) => { if (this.activeChild === this.childKey(name, param)) this.openComputer(row); }, onOpen: (page: string) => this.openSettings(page, NavigationContext.from(name, param as string).scoped) })
        else this.targetLoading(name, param)
      }
    }.title({ builder: () => this.destinationTitle(name, param), height: this.headerHeight() }, { systemMaterial: this.titleMaterial })
      .hideTitleBar(!this.wide() || this.hideChatTitle(name, param)).hideBackButton(this.destinationBackHidden(name, param))
      .backgroundColor(name === 'resources' && param === 'inbox' ? Colors.grouped : Colors.canvas)
      .menus(this.navigationMenus(name, param))
      .onShown(() => this.routeShown(name, param))
      .onHidden(() => { if (this.pendingInboxHandoff === this.childKey(name, param)) this.pendingInboxHandoff = ''; if (this.activeChild === this.childKey(name, param)) { this.activeChild = ''; this.dismissChatActions(); } if ((name === 'session' || name === 'conversation') && this.routeRow(param).key() === this.detailModel.selected.key()) this.detailModel.hideConversation(false); if (name === 'assistant') this.detailModel.watchDecisions(false); if (name === 'tools') this.detailModel.releaseTerminal(); })
      .onBackPressed((): boolean => { if (this.directionPermissionsVisible) {this.directionPermissionsVisible=false;return true;} if (this.messageLift.dismiss() || this.closeForwardNavigation()) return true; if (!this.spacesShown && this.returnToPhoneSettings()) return true; if (this.spacesShown) { this.spacesShown = false; return true; } if (this.pendingInboxHandoff === this.childKey(name, param)) this.pendingInboxHandoff = ''; this.phoneBack(); return true; })
  }
  private closeForwardNavigation(): boolean {
    if (this.chatForward.isChoosing(this.detailModel)) { this.chatForward.cancelPicker(); return true; }
    if (this.chatForward.isPicking(this.detailModel)) { this.chatForward.end(); return true; }
    return false;
  }
  @Monitor('showSheet') forwardFormVisibility(change:IMonitor): void {
    const visible=change.value<boolean>('showSheet');
    if (visible && visible.before && !visible.now && this.sheetKind==='newagent') this.chatForward.clearNew();
  }
  @Builder forwardPickerContent() {
    PhoneChatForwardPicker({source:this.detailModel,state:this.chatForward,onTarget:(row:ResourceRow):void => this.openPickedChat(row),
      onNewAgent:():void => { if (this.chatForward.newFor(this.model)) void this.prepareAgent(undefined,undefined,true); }})
  }
  onBackPress(): boolean {
    if (this.directionPermissionsVisible) { this.directionPermissionsVisible=false;return true; }
    if (this.messageLift.dismiss() || this.closeForwardNavigation()) return true;
    if (this.chatSearchShown) { this.chatSearchShown = false; return true; }
    if (this.phoneSettingsShown) {
      if (this.phoneSettingsPage) this.backPhoneSetting();
      else { this.phoneSettingsQuery = ''; this.phoneSettingsShown = false; }
      return true;
    }
    if (!this.spacesShown) return false;
    this.spacesShown = false; return true;
  }
  @Builder hoverDeckPill(label: string, tint: ResourceColor, identity: string, action: () => void) {
    Button(label, { type: ButtonType.Capsule })
      .height(34)
      .padding({ left: 12, right: 12 })
      .fontSize(Tokens.interfaceFontVp(12, this.environment.fontScale))
      .fontWeight(FontWeight.Medium)
      .backgroundColor(Colors.surface)
      .fontColor(tint)
      .border({ width: PhoneStyle.surfaceEdgeWidth(0.5), color: PhoneStyle.cardBorder(this.environment.dark), radius: 17 })
      .shadow(PhoneStyle.cardElevation(this.environment.dark))
      .onClick(() => {
        Haptics.tap();
        action();
      })
      .id(identity)
  }

  @Builder hoverTabletopDeck() {
    Column({ space: 10 }) {
      Row({ space: 8 }) {
        Text(tr('fold_posture_hover'))
          .fontSize(Tokens.interfaceFontVp(12, this.environment.fontScale))
          .fontWeight(FontWeight.Bold)
          .fontColor(Colors.link)
          .padding({ left: 10, right: 10, top: 4, bottom: 4 })
          .backgroundColor(Colors.selection)
          .borderRadius(Tokens.capsule)
        Text(tr('fold_hover_deck_title'))
          .fontSize(Tokens.interfaceFontVp(14, this.environment.fontScale))
          .fontWeight(FontWeight.Bold)
          .fontColor(Colors.ink)
          .layoutWeight(1)
        this.hoverDeckPill(tr('fold_relay_prev'), Colors.ink, 'fold.hover.prev', () => this.stepFoldRelay(-1))
        this.hoverDeckPill(tr('fold_relay_next'), Colors.link, 'fold.hover.next', () => this.stepFoldRelay(1))
      }.width('100%').alignItems(VerticalAlign.Center)

      Row({ space: 8 }) {
        this.hoverDeckPill(tr('files'), Colors.link, 'fold.hover.files', () => this.openTools('', 0))
        this.hoverDeckPill(tr('git'), Colors.link, 'fold.hover.git', () => this.openTools('', 1))
        this.hoverDeckPill(tr('terminal'), Colors.link, 'fold.hover.terminal', () => this.openTools('', 2))
        this.hoverDeckPill(tr('phone_xiaoyi'), Colors.ai, 'fold.hover.xiaoyi', () => this.openXiaoYiQuick())
        if (this.wide() && this.foldLayout.supportsInspector) {
          this.hoverDeckPill(tr('fold_inspector_title'), Colors.ink, 'fold.hover.inspector', () => this.toggleFoldInspector())
        }
      }.width('100%')
    }
    .width('100%')
    .layoutWeight(1)
    .padding({ left: 16, right: 16, top: 12, bottom: 16 })
    .justifyContent(FlexAlign.SpaceEvenly)
    .backgroundColor(Colors.grouped)
    .id('fold.hover.deck')
  }

  build() {
    Stack() {
    if (this.drawerFraction > 0 && this.fullHeightPhone() && !this.environment.keyboardHeightPx) {
      Row() {
        Row().width(this.drawerFraction * this.drawerWidth()).height('100%')
          .background(Colors.phoneDrawerSurface, { ignoresLayoutSafeAreaEdges: [LayoutSafeAreaEdge.BOTTOM] })
        Stack() {
          Row().width('100%').height('100%').background(Colors.canvas, { ignoresLayoutSafeAreaEdges: [LayoutSafeAreaEdge.BOTTOM] })
          Row().width('100%').height('100%').opacity(this.drawerFraction)
            .background(Colors.subtleScrim, { ignoresLayoutSafeAreaEdges: [LayoutSafeAreaEdge.BOTTOM] })
        }.layoutWeight(1).height('100%')
      }.width('100%').height('100%').hitTestBehavior(HitTestMode.None).accessibilityLevel('no-hide-descendants')
    }
    WithTheme({ theme: nativeLookTheme(this.look, this.model.style, this.environment.dark), colorMode: this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT }) {
    ThemeScope({ systemControls: this.look.accentId === 'style' && usesSystemStyleColors(this.model.style),
      styleID: phoneInterfaceStyleID(this.model.style),
      interfaceAccent: deviceLookInterfaceTint(this.look.accentId,this.model.style,this.environment.dark,
        this.environment.dark ? this.look.palette(this.model.style).dark : this.look.palette(this.model.style).light),
      surfaces: deviceLookSurface(this.model.style, this.look.accentId, this.environment.dark, this.look.palette(this.model.style)),
      selectedAccentInk: deviceLookSelectedFilterInk(this.look.palette(this.model.style), this.environment.dark) }) {
    Stack({alignContent:Alignment.TopStart}) {
      if (this.model.profile.id && !this.wide() && (this.spacesShown || this.drawerMounted)) {
        Column() { this.spacesContent() }.width(this.drawerWidth()).height('100%').backgroundColor(Colors.canvas)
          .accessibilityLevel(this.spacesShown ? 'auto' : 'no-hide-descendants')
      }
      Stack({alignContent:Alignment.TopStart}) {
    Row() {
    Column() {
      if (!this.model.profile.id) {
        Stack() {
          if (!this.wide()) PhoneFirstRun({error:this.model.error ? trError(this.model.error) : '',pendingCleanup:this.model.pendingCleanupCount>0,
            onScan:()=>this.openSheet('connect', 'scan'),
            onManual:()=>this.openSheet('connect', 'manual'),onDemo:()=>this.model.useDemo(),onCleanup:()=>this.model.retryCleanup()})
          else Column() {
            Column({ space: 12 }) {
              Image($r('app.media.foreground')).width(72).height(72).objectFit(ImageFit.Contain).borderRadius(18)
              Text(tr('persona')).fontSize(34).fontWeight(FontWeight.Bold).fontColor(Colors.ink)
              Text(tr('phone_welcome_intro')).fontSize(16).fontColor(Colors.secondary).textAlign(TextAlign.Center).margin({bottom:24})
              if (this.model.error) Text(trError(this.model.error)).fontSize(14).fontColor(Colors.danger)
              Button(tr('phone_connect_hub')).width('100%').height(48).fontSize(17).backgroundColor(Colors.action).fontColor(Colors.onAction)
                .onClick(() => this.openSheet('connect')).id('welcome.connect')
              Button(tr('phone_local_demo')).width('100%').height(46).fontSize(16).backgroundColor(Colors.surface).fontColor(Colors.link)
                .onClick(() => this.model.useDemo()).id('welcome.demo')
              if (this.model.pendingCleanupCount > 0) Button({ type: ButtonType.Circle }) { SymbolGlyph(IconActions.symbol('retry_cleanup')).fontSize(Tokens.symbolFont).fontColor([IconActions.color('retry_cleanup', false)]) }.attributeModifier(ButtonStyles.icon).accessibilityText(tr('retry_cleanup')).bindTips(tr('retry_cleanup')).onClick(() => this.model.retryCleanup())
            }.width('100%').constraintSize({ maxWidth: 440 }).alignItems(HorizontalAlign.Center)
          }.justifyContent(FlexAlign.Center).alignItems(HorizontalAlign.Center).width('100%').height('100%').padding({left:32,right:32}).backgroundColor(Colors.grouped)
        }.width('100%').height('100%')
          .bindSheet($$this.showSheet, this.sheetContent(), { title: this.sheetKind === 'newagent' || this.compactPairing() ? undefined : {title:this.sheetTitle()}, height: this.sheetKind === 'newagent' || this.compactPairing() ? this.compactFormHeight() : SheetSize.LARGE, keyboardAvoidMode: this.sheetKind === 'newagent' ? SheetKeyboardAvoidMode.RESIZE_ONLY : undefined, showClose: this.sheetKind !== 'newagent' && !this.compactPairing(), onWillDismiss:(d:DismissSheetAction) => { if (this.sheetKind === 'newagent' && this.agentPanel) this.closeAgentPanel(); else if (!this.creatingAgent) d.dismiss(); } })
      } else {
        Column() {
          if (!this.wide() && !this.floatingProjectHeader && !this.floatingDraftHeader && (this.phoneTop || this.model.section!==1 || !this.agentSearching) && this.phoneTop !== 'project_tree' && (this.phoneTop || !this.projectPlace() || !this.projectSearching)) this.phoneHeader()
          Navigation(this.stack) { this.primaryContent() }.title({ builder: this.peerTitle, height: this.headerHeight() }, { systemMaterial: this.titleMaterial }).titleMode(NavigationTitleMode.Mini)
            .hideBackButton(true).hideTitleBar(this.rail() || !this.wide()).menus([])
            .backgroundColor(Colors.canvas)
            .toolbarConfiguration([])
            .mode(this.wide() ? NavigationMode.Split : NavigationMode.Stack)
            .navBarWidth(this.rail() ? Tokens.collapsed : this.foldLayout.masterWidth)
            .navBarWidthRange(this.rail() ? [Tokens.collapsed, Tokens.collapsed] : [this.foldLayout.masterMin, this.foldLayout.masterMax])
            .minContentWidth(this.foldLayout.detailMin).enableDragBar(!this.rail()).navDestination(this.destination)
            .onAreaChange(() => this.updateBreakpoint()).width('100%').layoutWeight(1)

            .bindSheet($$this.showSheet, this.sheetContent(), { title: this.sheetKind === 'newagent' || this.compactPairing() ? undefined : {title:this.sheetTitle()}, height: this.sheetKind === 'newagent' || this.compactPairing() ? this.compactFormHeight() : SheetSize.LARGE, keyboardAvoidMode: this.sheetKind === 'newagent' ? SheetKeyboardAvoidMode.RESIZE_ONLY : undefined, showClose: this.sheetKind !== 'newagent' && !this.compactPairing(), onWillDismiss:(d:DismissSheetAction) => { if (this.sheetKind === 'newagent' && this.agentPanel) this.closeAgentPanel(); else if (!this.creatingAgent) d.dismiss(); } })
        }
        .width('100%')
        .height(this.hoverSplit.active ? this.hoverSplit.topPaneHeightVp : 'auto')
        .layoutWeight(this.hoverSplit.active ? 0 : 1)
        if (this.hoverSplit.active) {
          Row().width('100%').height(this.hoverSplit.creaseBandVp).backgroundColor(Colors.grouped).id('fold.hover.crease')
          Column() {
            this.hoverTabletopDeck()
          }.width('100%').layoutWeight(1).id('fold.hover.tabletop')
        }
      }
    }.layoutWeight(1).height('100%').backgroundColor(Colors.canvas)
      .accessibilityLevel(this.chatSearchShown || this.spacesShown && !this.wide() ? 'no-hide-descendants' : 'auto')
    if (this.showFoldInspector()) {
      FoldInspectorPanel({
        model: this.model,
        detailModel: this.detailModel,
        panelWidth: this.foldLayout.inspectorWidth,
        postureLabel: this.foldPostureLabel(),
        isTriFold: this.isTriFoldPosture(),
        initialTab: this.foldWorkbenchTab,
        tabSignal: this.foldWorkbenchTabSignal,
        pendingInterruptKey: this.workbenchInterruptKey,
        onClose: () => {
          this.setFoldInspectorOpen(false);
        },
        onOpenTools: () => this.openTools(),
        onOpenToolsTab: (tab: number) => {
          if (this.shownName === 'tools') this.detailModel.selectWorkspaceTab(tab);
          else this.openTools('', tab);
        },
        onOpenSession: (row: ResourceRow) => this.openPickedChat(row),
        onStepRelay: (direction: 1 | -1) => this.stepFoldRelay(direction),
        onInterruptSession: (row: ResourceRow) => { this.interruptSessionFromWorkbench(row); },
        onOpenXiaoYi: () => this.openXiaoYiQuick(),
        onOpenTokenUsage: () => { this.tokenUsageOpen = true; },
        onOpenLink: (address: string) => this.openReader(address)
      })
    }
    }.width('100%').height('100%')
      if (this.floatingProjectHeader || this.floatingDraftHeader) Column() { this.phoneHeader() }.width('100%')
        .backgroundBlurStyle(BlurStyle.COMPONENT_THIN).hitTestBehavior(HitTestMode.Transparent)
        .accessibilityLevel(this.spacesShown || this.chatSearchShown ? 'no-hide-descendants' : 'auto')
      if (this.drawerFraction > 0 && !this.wide()) Row().width('100%').height('100%')
        .backgroundColor(Colors.subtleScrim)
        .opacity(this.drawerFraction)
        .onClick(() => this.spacesShown = false).accessibilityText(tr('close')).id('spaces.dismiss')
      }.width('100%').height('100%').backgroundColor(Colors.canvas)
        .borderRadius(this.drawerFraction > 0 && !this.wide() ? 55 : 0).clip(this.drawerFraction > 0 && !this.wide())
        .shadow({radius:this.drawerFraction > 0 && !this.wide() ? 24 : 0,color:'#2E000000'})
        .translate({x:this.wide() ? 0 : this.drawerFraction * this.drawerWidth()})
      if (this.chatSearchShown && this.startSearchInAgents) Column() {
        this.phoneSearch()
      }.width('100%').height('100%').backgroundColor(Colors.grouped).zIndex(2).id('shell.chat_search')
      Column().width(0).height(0)
        .bindSheet(this.chatSearchShown && !this.startSearchInAgents, this.allChatsSheet(), {
          height:this.compactFormHeight(), preferType:SheetType.BOTTOM, showClose:false, dragBar:false, backgroundColor:Colors.canvas,
          onWillDismiss:(dismiss:DismissSheetAction) => { this.chatSearchShown=false; dismiss.dismiss(); }
        })
    }.width('100%').height('100%').padding({top:this.compactNavigationInset()}).clip(true)
      .backgroundColor(this.compactInsetSurface())
      .blur(this.messageLift.current ? this.getUIContext().vp2px(3) : 0)
      .accessibilityLevel(this.messageLift.current ? 'no-hide-descendants' : 'auto')
      .onAreaChange((_old:Area,now:Area) => {
        const nextWidth = Number(now.width);
        const nextHeight = Number(now.height);
        const changed = nextWidth !== this.phoneWidth || nextHeight !== this.phoneHeight;
        this.phoneWidth = nextWidth;
        this.phoneHeight = nextHeight;
        if (changed) this.updateBreakpoint();
      })
      .parallelGesture(PanGesture({fingers:1,direction:PanDirection.Horizontal,distance:12}).tag('phone.shell.pan')
        .onActionStart((event:GestureEvent) => this.startPhonePan(event))
        .onActionUpdate((event:GestureEvent) => this.movePhonePan(event.offsetX))
        .onActionEnd((event:GestureEvent) => this.endPhonePan(event.offsetX,event.velocityX))
        .onActionCancel(() => this.endPhonePan(0,0)))
      .onGestureRecognizerJudgeBegin((event: BaseGestureEvent, current: GestureRecognizer, peers: GestureRecognizer[]) =>
        this.judgePhonePan(event, current, peers), true)
      .bindSheet(this.wide() && this.spacesShown, this.spacesContent(), { title: { title: tr('conversation_spaces') }, height: SheetSize.LARGE, showClose: true,
        onAppear: () => { this.spacesLayoutReady = true; this.measureSpaces(); }, onDisappear: () => { this.spacesLayoutReady = false; if (this.wide()) this.spacesShown = false; } })
    }
    }
    Column().width(0).height(0).bindSheet($$this.quickPaletteOpen, this.quickPaletteSheet(), {
      height: SheetSize.LARGE,
      preferType: this.wide() ? SheetType.CENTER : SheetType.BOTTOM,
      dragBar: !this.wide(),
      showClose: false,
      backgroundColor: Colors.sheetBackground,
      onDisappear: () => { this.quickPaletteOpen = false; }
    })
    Column().width(0).height(0).bindSheet($$this.starredSheetShown, this.starredSheetContent(), {
      height: SheetSize.LARGE,
      preferType: this.wide() ? SheetType.CENTER : SheetType.BOTTOM,
      dragBar: false,
      showClose: false,
      backgroundColor: Colors.sheetBackground,
      onDisappear: () => { this.starredSheetShown = false; }
    })
    Column().width(0).height(0).bindSheet($$this.exportSheetShown, this.exportSheetContent(), {
      height: SheetSize.LARGE,
      preferType: this.wide() ? SheetType.CENTER : SheetType.BOTTOM,
      dragBar: false,
      showClose: false,
      backgroundColor: Colors.sheetBackground,
      onDisappear: () => { this.exportSheetShown = false; }
    })
    Column().width(0).height(0).bindSheet($$this.archivedSheetShown, this.archivedSheetContent(), {
      height: SheetSize.LARGE,
      preferType: this.wide() ? SheetType.CENTER : SheetType.BOTTOM,
      dragBar: false,
      showClose: false,
      backgroundColor: Colors.sheetBackground,
      onDisappear: () => { this.archivedSheetShown = false; }
    })
    Column().width(0).height(0).bindSheet($$this.standingAuthSheetShown, this.standingAuthSheetContent(), {
      height: SheetSize.LARGE,
      preferType: this.wide() ? SheetType.CENTER : SheetType.BOTTOM,
      dragBar: false,
      showClose: false,
      backgroundColor: Colors.sheetBackground,
      onDisappear: () => { this.standingAuthSheetShown = false; }
    })
    Column().width(0).height(0).bindSheet($$this.fullSearchSheetShown, this.fullSearchSheetContent(), {
      height: SheetSize.LARGE,
      preferType: this.wide() ? SheetType.CENTER : SheetType.BOTTOM,
      dragBar: false,
      showClose: false,
      backgroundColor: Colors.sheetBackground,
      onDisappear: () => { this.fullSearchSheetShown = false; }
    })
    Column().width(0).height(0).bindSheet($$this.autoChatsSheetShown, this.autoChatsSheetContent(), {
      height: SheetSize.LARGE,
      preferType: this.wide() ? SheetType.CENTER : SheetType.BOTTOM,
      dragBar: false,
      showClose: false,
      backgroundColor: Colors.sheetBackground,
      onDisappear: () => { this.autoChatsSheetShown = false; }
    })
    Column().width(0).height(0).bindSheet($$this.tokenUsageOpen, this.tokenUsageSheet(), {
      height: SheetSize.LARGE,
      preferType: this.wide() ? SheetType.CENTER : SheetType.BOTTOM,
      dragBar: !this.wide(),
      showClose: false,
      backgroundColor: Colors.sheetBackground,
      onDisappear: () => { this.tokenUsageOpen = false; }
    })
    Column().width(0).height(0).bindSheet(!this.model.profile.id && this.phoneSettingsShown, this.phoneSettings(), {
      height: this.wide() ? SheetSize.LARGE : this.compactFormHeight(),
      preferType: this.wide() ? SheetType.CENTER : SheetType.BOTTOM,
      showClose: false, dragBar: false, backgroundColor: Colors.sheetBackground,
      onWillDismiss: (dismiss: DismissSheetAction) => {
        if (dismiss.reason === DismissReason.PRESS_BACK && this.phoneSettingsPage) this.backPhoneSetting();
        else { this.phoneSettingsShown = false; dismiss.dismiss(); }
      },
      onDisappear: () => { this.phoneSettingsShown = false; }
    })
    Column().width(0).height(0).bindSheet($$this.directionPermissionsVisible,this.directionPermissionsSheet(),{
      title:{title:tr('phone_default_permissions')},height:SheetSize.LARGE,showClose:true,
      onDisappear:()=>{this.directionPermissionsVisible=false;this.directionPermissionsTarget='';}})
    Column().width(0).height(0).bindSheet($$this.activityVisible, this.backgroundActivitySheet(), {
      height: '97.4%', preferType: SheetType.BOTTOM, dragBar: false, showClose: false,
      onDisappear: () => { this.activityVisible = false; this.activityRow = undefined; } })
    Column().width(0).height(0).bindSheet(this.chatForward.isChoosing(this.detailModel),this.forwardPickerContent(),{
      height:forwardRestingDetent,detents:[forwardRestingDetent,SheetSize.LARGE],
      detentSelection:this.chatForward.expanded ? SheetSize.LARGE : forwardRestingDetent,
      preferType:SheetType.BOTTOM,dragBar:true,showClose:false,
      onDisappear:():void => { if (this.chatForward.isChoosing(this.detailModel)) this.chatForward.cancelPicker(); }})
    if (this.messageLift.current) {
      WithTheme({theme:nativeLookTheme(this.look,this.model.style,this.environment.dark),colorMode:this.environment.dark ? ThemeColorMode.DARK : ThemeColorMode.LIGHT}) {
        PhoneMessageLiftHostLayer({content:this.messageLift.current!.content})
      }
    }
    Column() {
      FloatingToastOverlay({
        reconnecting: !!this.model.profile.id && !this.model.demo && !this.model.connected,
        topOffset: this.wide() ? 14 : Math.max(58, this.compactNavigationInset() + 56)
      })
    }.width('100%').height('100%').alignItems(HorizontalAlign.Center).justifyContent(FlexAlign.Start).hitTestBehavior(HitTestMode.Transparent)
    }.width('100%').height('100%').backgroundColor(Colors.canvas)
      .background(this.compactInsetSurface(), { ignoresLayoutSafeAreaEdges: this.compactInsetEdges() })
      .onKeyEvent((event: KeyEvent) => this.handleFoldKeyEvent(event))
      .bindSheet($$this.readerShown, this.readerContent(), { title: { title: this.readerTitle }, height: Tokens.readerFull, width: Tokens.readerFull, preferType: SheetType.BOTTOM,
        detents: [Tokens.readerFull, Tokens.readerPartial], enableOutsideInteractive: true, showClose: true })
  }
}
