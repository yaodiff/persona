import type { BackgroundRound } from '../core/BackgroundActivity';
import { activityFeedRound, splitBackgroundRounds } from '../core/BackgroundActivity';
import type { HubAccess } from '../core/HubAccess';
import { liftedMessageWords } from '../core/Markdown';
import type { ConversationRecapItem } from './Wire';
import { AssistantViewModel } from './AssistantView';
import { SshTunnel } from './SshTunnel';
import type { SshProxyLease } from './SshTunnel';
import type { PairingSshRoute } from './PairingSsh';
import { KnowledgeRecording } from './KnowledgeRecording';
import { deviceLook } from './DeviceLook';
import { storedPhoneInterfaceStyle } from '../core/PhoneInterfaceStyle';
import { clientLocale, initializeClientLanguage, setClientLanguage } from '../core/ClientLocale';
import { dictationLocale, dictationTexts } from '../core/DictationLanguage';
import { ChatSendFeedback } from '../core/ChatSendFeedback';
import { dictationPreference } from '../platform/DictationPreference';
import { i18n } from '@kit.LocalizationKit';
import { DemoRepository } from './DemoRepository';
import { NavigationContext } from './NavigationContext';
import { utf8Bytes } from '../core/TranscriptCache';
import { ChatDraftStore, ChatDraft, ChatOutboxEntry, DraftForwardImage, draftForwardRows } from './ChatDraftStore';
import { clearImagePreviews } from '../platform/NativePreview';
import { forwardPrefix, forwardOrigin, forwardGroups } from '../core/ChatForwardWire';
import { parseMessageContent } from '../core/MessageContent';
import { decodeInteractionDecision, firstBlockingDecision, interactionCanSubmit, isPersonaAskUserQuestion } from './InteractionDecision';
import { ChatAttachments } from './ChatAttachments';
import { AgentAttentionCard, agentAttentionCards, waitingAgentBadge } from './AgentAttention';
import type { PreparedAttachmentSend } from './ChatAttachments';
import { nativeNotifications } from '../platform/NativeNotifications';
import { nativeLiveView } from '../platform/NativeLiveView';
import type { LiveViewPrefsStorage } from '../platform/NativeLiveView';
import { resolveManagerScope } from '../core/ScopePolicy';
import { SessionWorkspace } from './SessionWorkspace';
import { common } from '@kit.AbilityKit';
import { util } from '@kit.ArkTS';
import { HubTransport, EndpointConfig, HttpFailure } from '../platform/HubTransport';
import { CredentialVault } from '../platform/CredentialVault';
import { LocalStore } from '../platform/LocalStore';
import { HubRepository, segment } from './HubRepository';
import { Wire, ResourceRow, ChatMessage, Decision, HubProfile, HeldAgentMessage, ImageLimits } from './Wire';
import { tr, Strings } from '../design/Strings';
import { withSourceContext, messageDisplay } from './Compatibility';
import { MutationRecord, MutationStore } from './MutationStore';
import { TranscriptModel } from './TranscriptModel';
import { Pairing, PairingTarget } from '../platform/Pairing';
import { scopeKey, sessionCapabilities, projectToolTurn, TranscriptReducer } from '../core/Domain';
import type { SessionCapabilityInput, EffectiveSessionCapabilities, TranscriptItem, TranscriptSnapshot, ToolTurnProjection, WorkInspectionStep } from '../core/Domain';
import { extractPublishedModules, isRecordingModuleAllowed, parseStoredHubModules, serializeStoredHubModules } from '../core/XiaoYiActions';
import { LARK_MODULE_FEATURE, MAIL_MODULE_FEATURE, hasRegisteredPushRoute, isHubModuleAllowed, parseHiddenHubPages } from '../core/HubModules';
import { buildNextSuggestionsPayload, parseAssistantNextSuggestions, resolveAuthoritativeNextSuggestions } from '../core/NextSuggestions';




interface DevicePreferenceChange { appearance?: number; style?: string; alerts?: boolean; }
interface HeldActionReceipt { scope: string; pairing: number; target: string; expected: HeldAgentMessage; deleted: boolean; }

export interface ClientViewLease {
  readonly storageKey?:string;
  readonly epoch: number;
  valid: () => boolean;
  read: () => Promise<string>;
  write: (value: string) => Promise<void>;
  clearIfUnchanged: (expected: string) => Promise<void>;
}

class LocalLiveViewPrefsStorage implements LiveViewPrefsStorage {
  private readonly store: LocalStore;
  constructor(store: LocalStore) { this.store = store; }
  get(key: string): Promise<string | undefined> { return this.store.get(key); }
  put(key: string, value: string): Promise<void> { return this.store.put(key, value); }
}

class PhoneListRead {
  change: number = 0;
  snapshot: string = '';
  pendingKey: string = '';
  pending: Promise<void> | undefined;
}

@ObservedV2
export class ClientModel {
  readonly ssh: SshTunnel = new SshTunnel();
  private static pairingEpochs: Map<string, number> = new Map();
  private static forgetHooks: Set<(hubId: string, context: common.Context) => Promise<void>> = new Set();
  static registerForgetHook(hook: (hubId: string, context: common.Context) => Promise<void>): void {
    ClientModel.forgetHooks.add(hook);
  }
  private static statusHooks: Set<(pendingCount: number, workingCount: number, context: common.Context) => void> = new Set();
  static registerStatusHook(hook: (pendingCount: number, workingCount: number, context: common.Context) => void): void {
    ClientModel.statusHooks.add(hook);
  }
  private notifyStatusHooks(): void {
    if (this.detached || this.demo || !this.connected || !this.context || ClientModel.statusHooks.size === 0) return;
    const workingCount = this.sessions.filter((s: ResourceRow): boolean => s.state === 'working').length;
    const pendingCount = this.phoneAgentBadge((): boolean => false);
    for (const hook of ClientModel.statusHooks) {
      try { hook(pendingCount, workingCount, this.context); } catch (_) {}
    }
  }
  pairingRevision(): number { return ClientModel.pairingEpochs.get(this.profile.id) || 0; }
  private retirePairing(id: string): void { ClientModel.pairingEpochs.set(id, (ClientModel.pairingEpochs.get(id) || 0) + 1); }
  retireHubConnections(id: string): void {
    const root = this.directoryParent || this;
    if (root.profile.id === id) {
      root.stopReconnect(); root.reconnectAttempt = 0; root.generation++; root.selectionGeneration++; root.connected = false;
      root.clearPendingDecisions();
      root.ssh.stop(); root.repository?.close(); root.repository = undefined;
      root.releaseTerminal();
      root.scheduleReconnect();
    }
    root.retireViews(id);
    root.publishDirectory();
  }
  @Trace ready: boolean = false;
  @Trace projectsRequested: boolean = false;
  @Trace pairingLink: string = '';
  @Trace notificationTarget: string = '';
  @Trace pendingXiaoYiRoute: string = '';
  @Trace rememberedHubModules: string[] | null = null;
  @Trace useHerdrTabName: boolean = false;
  @Trace agentsDisplaySupported: boolean = false;
  @Trace agentsDisplaySaving: boolean = false;
  private agentsDisplayRevision: number = 0;
  @Trace nextSuggestionsValue: boolean | null = null;
  @Trace nextSuggestionsLoaded: boolean = false;
  @Trace nextSuggestionsSaving: boolean = false;
  @Trace nextSuggestionsError: string = '';
  private nextSuggestionsRevision: number = 0;
  openXiaoYiRoute(routeJson: string): void {
    if (!routeJson || routeJson.length > 16384) return;
    this.pendingXiaoYiRoute = routeJson;
  }
  private hubModulesKey(hubId: string): string {
    return 'xiaoyi:' + scopeKey({ groupId: hubId, managerId: '' }) + ':hub-modules';
  }
  private agentsDisplayKey(hubId: string): string {
    return 'view:' + scopeKey({ groupId: hubId, managerId: '' }) + ':agents-display';
  }
  isRecordingModuleAllowed(): boolean {
    return isRecordingModuleAllowed(this.features, this.rememberedHubModules);
  }
  canPreviewDemoRecordings(): boolean {
    return this.demo;
  }
  mailModuleAllowed(): boolean {
    return this.demo || isHubModuleAllowed(MAIL_MODULE_FEATURE, this.features, this.rememberedHubModules);
  }
  larkModuleAllowed(): boolean {
    return this.demo || isHubModuleAllowed(LARK_MODULE_FEATURE, this.features, this.rememberedHubModules);
  }
  inboxPageVisible(): boolean {
    return this.demo || !parseHiddenHubPages(this.sidebarConfig).has('inbox');
  }
  todosPageVisible(): boolean {
    const hidden = parseHiddenHubPages(this.sidebarConfig);
    return this.demo || (!hidden.has('todos') && !hidden.has('todo'));
  }
  knowledgePageVisible(): boolean {
    return this.demo || !parseHiddenHubPages(this.sidebarConfig).has('knowledge');
  }
  /**
   * Whether this device has a registered Hub background push route (#16783, PushHealth).
   * HarmonyOS currently uses foreground `ws_notify` without Push Kit registration yet,
   * so non-demo sessions return `false` and end active LiveViews on background instead of leaving them frozen.
   */
  hasPushRoute(): boolean {
    return this.demo || hasRegisteredPushRoute(false);
  }
  @Trace forgottenProfile: string = '';
  @Trace forgottenEpoch: number = 0;
  @Trace busy: boolean = false;
  @Trace error: string = '';
  @Trace diagnosticCode: string = '';
  @Trace connected: boolean = false;
  @Trace demo: boolean = false;
  @Trace profiles: HubProfile[] = [];
  @Trace profile: HubProfile = new HubProfile();
  @Trace managers: ResourceRow[] = [];
  @Trace rosterDefaultId: string = '';
  @Trace features: string[] = [];
  @Trace starredRevision: number = 0;
  @Trace private publishedImageLimits: Record<string, Object> = {};
  private helloHub: string = '';
  private helloScope: string = '';
  private helloGeneration: number = -1;
  private helloVersion: string = '';
  private helloFeatures: string[] = [];
  @Trace section: number = 0;
  @Trace sessions: ResourceRow[] = [];
  @Trace sessionsReady: boolean = false;
  @Trace private attentionCards: AgentAttentionCard[] = [];
  @Trace private attentionOwner: string = '';
  private attentionReadRevision: number = 0;
  @Trace conversations: ResourceRow[] = [];
  @Trace namingAgentKeys: string[] = [];
  @Trace machines: ResourceRow[] = [];
  @Trace directorySessions: ResourceRow[] = [];
  @Trace directoryMachines: ResourceRow[] = [];
  @Trace directoryLoading: boolean = false;
  @Trace directoryUnavailable: number = 0;
  @Trace directoryReady: boolean = false;
  private directoryTask: Promise<void> | undefined;
  private directoryOwner: string = '';
  private directoryParent: ClientModel | undefined;
  @Trace todos: ResourceRow[] = [];
  @Trace knowledge: ResourceRow[] = [];
  @Trace knowledgeKind: string = 'projects';
  @Trace inbox: ResourceRow[] = [];
  private inboxRead: PhoneListRead = new PhoneListRead();
  private todoRead: PhoneListRead = new PhoneListRead();
  @Trace selected: ResourceRow = new ResourceRow();
  @Trace messages: ChatMessage[] = [];
  @Trace cards: ResourceRow[] = [];
  @Trace decision: Decision | undefined;
  @Trace pendingDecisions: Decision[] = [];
  @Trace personaQuestions: Decision[] = [];
  private pendingDecisionTarget: string = '';
  @Trace private decisionReadyTarget: string = '';
  private decisionRefreshSequence: number = 0;
  @Trace draft: string = '';
  @Trace draftRevision: string = '';
  private localMessages: ChatOutboxEntry[] = [];
  private activeMessages: Set<string> = new Set();
  private sendAttempts: Map<string, ChatSendFeedback> = new Map();
  @Trace private sendFeedbackRevision: number = 0;
  private feedbackTimer: number = -1;
  private feedbackDeadline: number = 0;
  private renderedMessages: ChatMessage[] = [];
  private echoedMessages: Set<string> = new Set();
  @Trace private heldActions: string[] = [];
  @Trace private heldReceipts: HeldActionReceipt[] = [];
  private heldRevisions: Map<string, number> = new Map();
  private heldOwner(): ClientModel { return this.directoryParent ? this.directoryParent.heldOwner() : this; }
  private heldRevision(scope: string): number { return this.heldRevisions.get(scope) || 0; }
  private heldChanged(scope: string): void { this.heldRevisions.set(scope, this.heldRevision(scope) + 1); }
  @Trace draftLoading: boolean = false;
  @Trace sourceContext: string = '';
  @Trace reply: string = '';
  @Trace operationState: string = '';
  @Trace mainId: string = '';
  @Trace mainMachineId: string = '';
  @Trace sidebarConfig: string = '';
  acceptSidebarConfig(assistant: Record<string,Object>, confirmedEdit: boolean = false): void {
    const existing = Wire.parse(this.sidebarConfig);
    const payload = Wire.payload(['main','main_machine_id','leaders','background_supervisor','recent_task_chats'],
      [Wire.text(assistant,'main'),Wire.text(assistant,'main_machine_id',Wire.text(assistant,'machine_id')),
        Wire.strings(assistant,'leaders'),Wire.text(assistant,'background_supervisor'),Wire.bool(assistant,'recent_task_chats')]);
    if (Wire.has(assistant, 'pages')) payload['pages'] = assistant['pages'];
    else if (Wire.has(existing, 'pages')) payload['pages'] = existing['pages'];
    if (Wire.has(assistant, 'hidden')) payload['hidden'] = assistant['hidden'];
    else if (Wire.has(existing, 'hidden')) payload['hidden'] = existing['hidden'];
    if (Wire.has(assistant, 'hidden_pages')) payload['hidden_pages'] = assistant['hidden_pages'];
    else if (Wire.has(existing, 'hidden_pages')) payload['hidden_pages'] = existing['hidden_pages'];
    const config = JSON.stringify(payload);
    if (this.sidebarConfig !== config) this.sidebarConfig = config;
    // A scoped settings page can use a detached model. Publish its confirmed Hub preference to the visible drawer only for the same owner.
    const parent = this.directoryParent;
    if (confirmedEdit && parent && parent.scope() === this.scope() && parent.profile.hubId === this.profile.hubId) {
      const current = Wire.parse(parent.sidebarConfig);
      current['recent_task_chats'] = Wire.bool(assistant,'recent_task_chats');
      parent.sidebarConfig = JSON.stringify(current);
    }
  }
  applyNavigationConfig(data: Record<string, Object>): void {
    const existing = Wire.parse(this.sidebarConfig);
    const current: Record<string, Object> = Wire.payload([], []);
    Object.keys(existing).filter((key: string) => !['pages', 'hidden', 'hidden_pages'].includes(key)).forEach((key: string) => {
      current[key] = existing[key];
    });
    if (Wire.has(data, 'pages')) current['pages'] = data['pages'];
    if (Wire.has(data, 'hidden')) current['hidden'] = data['hidden'];
    if (Wire.has(data, 'hidden_pages')) current['hidden_pages'] = data['hidden_pages'];
    const next = JSON.stringify(current);
    if (this.sidebarConfig !== next) this.sidebarConfig = next;
  }
  private preferenceTask: Promise<void> = Promise.resolve();
  private async changePreferences(action: () => Promise<void>): Promise<void> { const task = this.preferenceTask.then(action, action); this.preferenceTask = task.catch(() => {}); await task; }
  @Trace language: string = 'default';
  @Trace languageChanging: boolean = false;
  @Trace appearance: number = 0;
  @Trace style: string = 'system';
  @Trace alerts: boolean = true;
  @Trace terminalText: string = '';
  @Trace terminalControl: boolean = false;
  @Trace resourceRows: ResourceRow[] = [];
  @Trace detail: string = '';
  @Trace hasMoreMessages: boolean = false;
  private deviceEvent: ((payload: string) => void) | undefined;
  private eventListeners: Set<(payload: string, type: string) => void> = new Set();
  observeEvents(listener: (payload: string, type: string) => void): () => void { this.eventListeners.add(listener); return (): void => { this.eventListeners.delete(listener); }; }
  setDeviceEvents(handler: ((payload: string) => void) | undefined): void { this.deviceEvent = handler; }
  private workspace: SessionWorkspace | undefined;
  setWorkspace(workspace: SessionWorkspace | undefined): void { if (this.workspace && this.workspace !== workspace) this.workspace.hide(); this.workspace = workspace; }
  selectWorkspaceTab(index: number): void { this.workspace?.selectTab(index); }
  @Trace transcriptHistoryReady: boolean = false;
  private transcript: TranscriptModel | undefined;
  private transcriptKey: string = '';
  private revisionItems: TranscriptItem[] = [];
  transcriptItems(): readonly TranscriptItem[] { return this.revisionItems.slice(); }

  private activityOwner: string = '';
  private activityRounds: readonly BackgroundRound[] = [];

  mainActivityRow(): ResourceRow | undefined {
    if (!this.mainId) return undefined;
    return this.conversations.find((row: ResourceRow): boolean => this.isMainActivityRow(row));
  }

  private isMainActivityRow(row: ResourceRow): boolean {
    return row.kind === 'conversation' && !!this.mainId && row.id === this.mainId &&
      (!this.mainMachineId || row.machine === this.mainMachineId) && (!row.group || row.group === this.profile.id) &&
      (!row.manager || row.manager === this.profile.manager);
  }

  readActivityTranscript(row: ResourceRow, publish: (items: readonly TranscriptItem[], more: boolean, ready: boolean) => void,
    failure: (error: Error) => void): TranscriptModel | undefined {
    const main = this.mainActivityRow();
    if (!main || this.target(main) !== this.target(row)) throw new Error('request_updated');
    if (this.demo && this.demoRepository) {
      publish(this.demoRepository.historyItems(row) as Object as TranscriptItem[], false, true);
      return undefined;
    }
    return new TranscriptModel(row, this.repo(), this.local, this.target(row), publish, failure, false);
  }

  feedInBackground(kind: string, at: number): boolean {
    return this.activityOwner === this.target() && !!activityFeedRound(this.activityRounds, kind, at);
  }
  private repository: HubRepository | undefined;
  private demoRepository: DemoRepository | undefined;
  private detached: boolean = false;
  private retired: boolean = false;
  private views: Map<string, ClientModel> = new Map();
  private viewTasks: Map<string, Promise<ClientModel>> = new Map();

  // A detail context retains its paired Hub and manager without changing the workspace selection.
  async viewFor(row: ResourceRow): Promise<ClientModel> {
    const group = row.group || this.profile.id;
    if (group === this.profile.id && (!row.manager || row.manager === this.profile.manager)) return this;
    if (this.retired || !this.profile.id || this.demo || this.forgettingProfiles.has(group) || this.cleanupProfiles.has(group)) throw new Error('request_updated');
    const source = group === this.profile.id ? this.profile : this.profiles.find((profile: HubProfile) => profile.id === group);
    if (!source) throw new Error('request_updated');
    const manager = row.manager || source.manager, key = JSON.stringify([group, manager]), existing = this.views.get(key);
    if (existing && !existing.retired && existing.connected) return existing;
    const taskKey = JSON.stringify([this.generation, key]), pending = this.viewTasks.get(taskKey); if (pending) return pending;
    const generation = this.generation, scope = this.scope();
    const task = this.connectView(manager, key, generation, scope, source);
    this.viewTasks.set(taskKey, task);
    try { return await task; } finally { if (this.viewTasks.get(taskKey) === task) this.viewTasks.delete(taskKey); }
  }
  private async connectView(manager: string, key: string, generation: number, scope: string, source: HubProfile = this.profile): Promise<ClientModel> {
    let view = this.views.get(key);
    if (!view || view.retired) {
      view = new ClientModel(); view.detached = true; view.local = this.local; view.vault = this.vault; view.context = this.context;
      if (this.context) view.ssh.initialize(this.context);
      view.ready = this.ready; view.foreground = this.foreground; view.appearance = this.appearance; view.style = this.style; view.alerts = false;
      view.profiles = this.profiles.slice(); view.directoryParent = this; this.views.set(key, view);
    }
    const profile = new HubProfile(source.id, source.name, source.baseUrl);
    profile.addresses = source.addresses.slice(); profile.hubId = source.hubId; profile.pin = source.pin; profile.manager = manager;
    await view.selectProfile(profile, true);
    this.publishDirectory();
    if (!this.owns(generation, scope) || this.forgettingProfiles.has(source.id) || view.retired || !view.connected) throw new Error(view.error || 'request_updated');
    return view;
  }
  directoryOwnerFor(row: ResourceRow): ClientModel | undefined {
    const group = row.group || this.profile.id;
    if (group === this.profile.id && row.manager === this.profile.manager) return this;
    return Array.from(this.views.values()).find((view: ClientModel) => !view.retired && view.profile.id === group && view.profile.manager === row.manager);
  }
  directoryOwners(): ClientModel[] { const owners: ClientModel[] = [this]; for (const owner of this.views.values()) if (!owner.retired && !this.forgettingProfiles.has(owner.profile.id)) owners.push(owner); return owners; }
  groupName(row: ResourceRow): string { return this.profiles.find((profile: HubProfile) => profile.id === (row.group || this.profile.id))?.name || this.profile.name; }
  async refreshDirectory(): Promise<void> {
    if (this.detached || this.demo) { this.publishDirectory(); return; }
    const generation = this.generation, scope = this.scope(), owner = JSON.stringify([generation, scope]);
    if (this.directoryTask && this.directoryOwner === owner) return this.directoryTask;
    const task = this.loadDirectory(generation, scope); this.directoryTask = task; this.directoryOwner = owner;
    try { await task; } finally { if (this.directoryTask === task) this.directoryTask = undefined; }
  }
  private async loadDirectory(generation: number, scope: string): Promise<void> {
    this.directoryLoading = true; let unavailable = 0;
    const profiles = this.profiles.slice();
    try {
      for (const profile of profiles) {
        if (!this.owns(generation, scope)) return;
        if (this.forgettingProfiles.has(profile.id)) continue;
        const target = new ResourceRow(); target.group = profile.id; target.manager = profile.manager;
        let view: ClientModel;
        try { view = await this.viewFor(target); if (view.connected) await view.refresh(); else unavailable++; }
        catch (_) { unavailable++; continue; }
        if (!this.owns(generation, scope)) return;
        const managers = view.managers.slice();
        for (const manager of managers) {
          if (!this.owns(generation, scope)) return;
          if (manager.id === view.profile.manager) continue;
          const scoped = new ResourceRow(); scoped.group = profile.id; scoped.manager = manager.id;
          try { const child = await this.viewFor(scoped); if (child.connected) await child.refresh(); else unavailable++; } catch (_) { unavailable++; }
        }
      }
    } finally {
      if (this.owns(generation, scope)) { this.directoryLoading = false; this.directoryUnavailable = unavailable; this.directoryReady = true; this.publishDirectory(); }
    }
  }
  private directoryRow(row: ResourceRow, view: ClientModel): ResourceRow {
    const copy = NavigationContext.rowFrom(JSON.stringify(row)); copy.group = view.profile.id;
    if (!view.connected) copy.state = 'offline'; return copy;
  }
  private publishDirectory(): void {
    if (this.directoryParent) { this.directoryParent.publishDirectory(); return; }
    const sessions = new Map<string, ResourceRow>(), machines = new Map<string, ResourceRow>();
    const profiles = new Set<string>(this.profiles.map((profile: HubProfile) => profile.id));
    if (this.demo) profiles.add(this.profile.id);
    const views: ClientModel[] = [this]; for (const view of this.views.values()) views.push(view);
    for (const view of views) {
      if (view.retired || !profiles.has(view.profile.id) || this.forgettingProfiles.has(view.profile.id)) continue;
      for (const row of view.sessions) { const next = this.directoryRow(row, view); if (!Wire.bool(next.data(), 'assistant')) sessions.set(next.key(), next); }
      for (const row of view.machines) { const next = this.directoryRow(row, view); machines.set(next.key(), next); }
    }
    this.directorySessions = Array.from(sessions.values()); this.directoryMachines = Array.from(machines.values());
  }
  private retireViews(id: string = ''): void {
    for (const key of this.views.keys()) {
      const view = this.views.get(key); if (!view || id && view.profile.id !== id) continue;
      view.retired = true; view.generation++; view.selectionGeneration++;
      view.background().catch(() => {}); this.views.delete(key);
    }
    this.viewTasks.clear();
  }
  private refreshTask: Promise<void> | undefined;
  private refreshOwner: string = '';
  private vault: CredentialVault | undefined;
  private local: LocalStore = new LocalStore();
  private context: common.UIAbilityContext | undefined;
  private generation: number = 0;
  private selectionGeneration: number = 0;
  @Trace unresolvedWrites: MutationRecord[] = [];

  private forgettingProfiles: Set<string> = new Set();
  private cleanupProfiles: Map<string, string> = new Map();
  @Trace pendingCleanupCount: number = 0;
  private async saveCleanup(): Promise<void> {
    await this.changePreferences(async (): Promise<void> => {
      await this.local.put('hub-cleanup', JSON.stringify(Array.from(this.cleanupProfiles.entries())));
    });
    this.pendingCleanupCount = this.cleanupProfiles.size;
  }
  private async restoreCleanup(): Promise<void> {
    const pendingCleanup = await this.local.get('hub-cleanup');
    if (pendingCleanup) {
      if (pendingCleanup.length > 65536) throw new Error('cleanup_incomplete');
      const pending = JSON.parse(pendingCleanup) as string[][];
      if (!Array.isArray(pending) || pending.length > 128) throw new Error('cleanup_incomplete');
      for (const pair of pending) {
        if (!Array.isArray(pair) || pair.length !== 2 || pair.some((value: string): boolean => typeof value !== 'string' || !value || value.length > 128 || /[\u0000-\u001f\u007f]/.test(value))) throw new Error('cleanup_incomplete');
        this.cleanupProfiles.set(pair[0], pair[1]);
      }
      this.pendingCleanupCount = this.cleanupProfiles.size;
    }
  }
  async retryCleanup(): Promise<void> {
    for (const id of Array.from(this.cleanupProfiles.keys())) { try { await this.forgetProfile(id); } catch (_) {} }
  }
  private pairingProfiles: Set<string> = new Set();
  private forgetEpochs: Map<string, number> = new Map();
  private credentialWrites: Map<string, Promise<void>> = new Map();
  private pendingNotification: Record<string, Object> | undefined;
  private notificationEpoch: number = 0;
  private async credentialChange(id: string, action: () => Promise<void>): Promise<void> {
    const previous = this.credentialWrites.get(id) || Promise.resolve();
    const work = previous.catch(() => {}).then(action), tail = work.catch(() => {});
    this.credentialWrites.set(id, tail);
    try { await work; } finally { if (this.credentialWrites.get(id) === tail) this.credentialWrites.delete(id); }
  }
  private feedTimer: number = -1;
  private draftTimer: number = -1;
  private foreground: boolean = true;
  private reconnectTimer: number = -1;
  private reconnectAttempt: number = 0;
  private stopReconnect(): void { if (this.reconnectTimer !== -1) clearTimeout(this.reconnectTimer); this.reconnectTimer = -1; }
  private scheduleReconnect(): void {
    if (this.error === 'cloudflare_access_required' || this.error === 'cloudflare_access_rejected') return;
    if (!this.foreground || this.demo || !this.profile.id || this.cleanupProfiles.has(this.profile.id) || this.reconnectTimer !== -1 || this.pairingProfiles.size > 0) return;
    const generation = this.generation, scope = this.scope(), delay = Math.min(30000, 1000 * Math.pow(2, Math.min(5, this.reconnectAttempt++)));
    this.reconnectTimer = setTimeout(() => { this.reconnectTimer = -1;
      if (this.foreground && this.owns(generation, scope) && !this.connected) this.selectProfile(this.profile, true).catch((error: Error) => this.failure(error));
    }, delay);
  }
  private chatVisible: boolean = false;
  private decisionsVisible: boolean = false;
  watchDecisions(visible: boolean): void { this.decisionsVisible = visible; if (visible && this.connected && this.isChat()) this.refreshDecisions().catch((error: Error) => this.failure(error)); }
  private clearPendingDecisions(): void {
    this.decisionRefreshSequence++;
    this.pendingDecisionTarget = '';
    this.decisionReadyTarget = '';
    this.pendingDecisions = [];
    this.personaQuestions = [];
    this.decision = undefined;
  }
  private async readInteractionSnapshot(): Promise<Record<string, Object>[]> {
    const generation = this.generation, scope = this.scope(), revision = ++this.attentionReadRevision;
    const values = await this.repo().decisions();
    if (revision === this.attentionReadRevision && this.owns(generation, scope)) {
      this.attentionCards = agentAttentionCards(values, this.profile.manager);
      this.attentionOwner = JSON.stringify([generation, scope]);
      this.notifyStatusHooks();
    }
    return values;
  }
  // Old briefing rows name a waiting card without its chat. Resolve it only from this owner's current snapshot.
  briefWaitingSession(cardID: string): ResourceRow | undefined {
    if (!cardID || !this.connected || this.attentionOwner !== JSON.stringify([this.generation, this.scope()])) return undefined;
    const matches = this.attentionCards.filter((card: AgentAttentionCard) => card.id === cardID && !!card.session);
    if (matches.length !== 1) return undefined;
    const card = matches[0];
    return this.sessions.find((row: ResourceRow) => row.manager === this.profile.manager &&
      (!row.group || row.group === this.profile.id) && row.machine === card.machine && (row.session || row.id) === card.session);
  }

  phoneAgentBadge(muted: (row: ResourceRow, space: string) => boolean): number {
    if (!this.connected) return 0;
    const current = this.attentionOwner === JSON.stringify([this.generation, this.scope()]);
    return waitingAgentBadge(this.sessions, this.todos, current ? this.attentionCards : [],
      this.profile.id, this.profile.manager, muted, current);
  }
  private replacePendingDecisions(interactions: Record<string, Object>[], row: ResourceRow, target: string,
    generation: number, scope: string, sequence: number): void {
    if (!this.owns(generation, scope) || this.target() !== target || sequence !== this.decisionRefreshSequence) return;
    const next: Decision[] = [], seen = new Set<string>(), previous = this.pendingDecisions;
    const session = row.session || row.id;
    for (const data of interactions) {
      const nested = Wire.object(data, 'interaction');
      const machine = Wire.text(data, 'machine_id', Wire.text(nested, 'machine_id'));
      const sessionId = Wire.text(data, 'session_id', Wire.text(nested, 'session_id'));
      const manager = Wire.text(data, 'manager', Wire.text(data, 'manager_id'));
      if (machine !== row.machine || sessionId !== session || manager && manager !== this.profile.manager) continue;
      let request = this.decodeDecision(data);
      request.machine = machine; request.session = sessionId;
      if (!request.id || seen.has(request.id)) continue;
      seen.add(request.id);
      const old = previous.find((item: Decision): boolean => item.id === request.id);
      if (old && old.revision === request.revision && old.raw === request.raw) {
        if (old.status === 'resolved' && request.status === 'open' || old.status === 'pending' && request.status === 'open') {
          request = old;
        } else {
          old.status = request.status;
          request = old;
        }
      }
      next.push(request);
    }
    this.pendingDecisionTarget = target;
    this.pendingDecisions = next;
    this.personaQuestions = next.filter((request: Decision): boolean => isPersonaAskUserQuestion(request, this.selected.kind));
    this.decision = firstBlockingDecision(next, this.selected.kind);
    this.decisionReadyTarget = target;
  }
  async refreshDecisions(): Promise<void> {
    const generation = this.generation, scope = this.scope(), target = this.target(), row = this.selected;
    const sequence = ++this.decisionRefreshSequence;
    try {
      const interactions = await this.readInteractionSnapshot();
      this.replacePendingDecisions(interactions, row, target, generation, scope, sequence);
    } catch (error) {
      if (this.owns(generation, scope) && this.target() === target && sequence === this.decisionRefreshSequence) this.failure(error as Error);
      throw error as Error;
    }
  }

  // A device preference shared by Persona's conversation docks across Hubs and rotations.
  @Trace questionCardHeightShare: number = -1;
  async setQuestionCardHeight(share: number): Promise<void> {
    if (!Number.isFinite(share)) return;
    this.questionCardHeightShare = share < 0 ? -1 : Math.max(0,Math.min(1,share));
    const stored = this.questionCardHeightShare;
    await this.changePreferences(async (): Promise<void> => {
      if (stored < 0) await this.local.remove('ui.questionCardHeight');
      else await this.local.put('ui.questionCardHeight',String(stored));
    });
  }

  async initialize(context: common.UIAbilityContext): Promise<void> {
    this.context = context;
    dictationPreference.load(context);
    this.ssh.initialize(context);
    this.vault = new CredentialVault(context);
    await this.local.open(context);
    if (!this.detached) {
      nativeLiveView.initialize(context, new LocalLiveViewPrefsStorage(this.local)).catch(() => {});
    }
    const storedCardHeight = await this.local.get('ui.questionCardHeight');
    const share = Number(storedCardHeight);
    if (storedCardHeight && Number.isFinite(share)) this.questionCardHeightShare = Math.max(0,Math.min(1,share));
    const preferences = Wire.parse(await this.local.get('preferences'));
    this.appearance = Wire.number(preferences, 'appearance');
    const savedStyle = Wire.text(preferences, 'style', 'system');
    this.style = storedPhoneInterfaceStyle(savedStyle);
    this.alerts = Wire.bool(preferences, 'alerts', true); this.language = Wire.text(preferences, 'language', 'default');
    if (!['default', 'en-US', 'zh-CN', 'ja-JP'].includes(this.language)) this.language = 'default';
    i18n.System.setAppPreferredLanguage(this.language); initializeClientLanguage(this.language);
    await nativeNotifications.initialize(context);
    const saved = Wire.parse(await this.local.get('profiles'));
    await this.restoreCleanup();
    this.profiles = Wire.rows(saved, 'profiles').map((item: Record<string, Object>) => {
      const profile = new HubProfile(Wire.text(item, 'id'), Wire.text(item, 'name'), Wire.text(item, 'baseUrl'));
      profile.addresses = Wire.strings(item, 'addresses'); profile.hubId = Wire.text(item, 'hubId'); profile.pin = Wire.text(item, 'pin'); profile.manager = Wire.text(item, 'manager');
      return profile;
    });
    this.ready = true;
    await this.retryCleanup();
    const active = Wire.text(saved, 'active');
    const profile = this.profiles.find((item: HubProfile) => item.id === active);
    if (profile) { if (this.foreground) await this.selectProfile(profile); else this.profile = profile; }
  }
  scope(): string { return scopeKey({ groupId: this.profile.id, managerId: this.profile.manager }); }
  target(row: ResourceRow = this.selected): string { return JSON.stringify([this.scope(), row.kind, row.machine, row.id]); }
  repo(): HubRepository {
    if (!this.repository || !this.connected) throw new Error('offline');
    return this.repository;
  }
  connectionGeneration(): number { return this.generation; }
  verifiedServerVersion(feature: string): string {
    return this.connected && !!this.helloHub && this.helloHub === this.profile.hubId &&
      this.owns(this.helloGeneration, this.helloScope) && this.helloFeatures.includes(feature) ? this.helloVersion : '';
  }
  owns(generation: number, scope: string): boolean { return !this.retired && generation === this.generation && scope === this.scope(); }
  isChat(): boolean { return this.selected.kind === 'conversation' || this.selected.kind === 'session'; }
  isAppInForeground(): boolean { return !this.retired && this.foreground; }
  isForegroundChat(): boolean { return !this.retired && this.foreground && this.chatVisible && this.isChat(); }
  reportError(error: Error): void { this.failure(error); }
  operationBlocked(path: string): boolean { return this.unresolvedWrites.some((record: MutationRecord) => record.path === path); }
  private failure(error: Error): void {
    if (error instanceof HttpFailure) {
      if (error.code === 'cloudflare_access_required' || error.code === 'cloudflare_access_rejected') {
        this.stopReconnect(); this.error = error.resultUnknown ? 'result_unknown' : error.code; return;
      }
      if (error.status === 401 || error.status === 403) this.stopReconnect();
      this.error = error.resultPending ? 'pending' : error.resultUnknown ? 'result_unknown' : error.status === 401 ? 'welcome' :
        error.status === 403 ? 'permission_denied' : error.status === 409 || error.status === 410 ? 'request_updated' : 'failed';
    } else this.error = ['offline', 'pending_action'].includes(error.message) ? error.message : 'failed';
  }
  private async saveProfiles(): Promise<void> {
    if (this.detached) return;
    await this.local.put('profiles', JSON.stringify({ profiles: this.profiles, active: this.profile.id }));
  }
  private async applyAppearance(change: DevicePreferenceChange): Promise<boolean> {
    let saved = false;
    await this.changePreferences(async (): Promise<void> => {
      const mode = change.appearance === undefined ? this.appearance : change.appearance;
      const requested = change.style === undefined ? this.style : change.style;
      const style = storedPhoneInterfaceStyle(requested);
      const alerts = change.alerts === undefined ? this.alerts : change.alerts;
      try { await this.local.put('preferences', JSON.stringify({ appearance: mode, style: style, alerts: alerts, language: this.language })); }
      catch (_) { this.error = 'cache_unavailable'; return; }
      this.appearance = mode; this.style = style; this.alerts = alerts;
      for (const view of this.views.values()) { view.appearance = mode; view.style = style; }
      saved = true;
    });
    return saved;
  }
  async setAppearance(mode: number, style?: string, alerts?: boolean): Promise<boolean> { return await this.applyAppearance({ appearance: mode, style: style, alerts: alerts }); }
  async setStyle(style: string): Promise<void> { await this.applyAppearance({ style: style }); }
  async setAlerts(alerts: boolean): Promise<void> { await this.applyAppearance({ alerts: alerts }); }
  async setLanguage(language: string): Promise<void> {
    if (this.languageChanging || !['default', 'en-US', 'zh-CN', 'ja-JP'].includes(language)) return;
    this.languageChanging = true; let applied = false;
    try {
      await this.changePreferences(async (): Promise<void> => {
        const previous = this.language;
        const effective = clientLocale(i18n.getSystemLanguage());
        const previousNative = previous === 'default' ? (effective === 'zh-Hans' ? 'zh-CN' : effective === 'ja' ? 'ja-JP' : 'en-US') : previous;
        i18n.System.setAppPreferredLanguage(language); applied = true;
        try { await this.local.put('preferences', JSON.stringify({ appearance: this.appearance, style: this.style, alerts: this.alerts, language: language })); }
        catch (error) {
          i18n.System.setAppPreferredLanguage(previousNative);
          if (previous === 'default') i18n.System.setAppPreferredLanguage('default');
          setClientLanguage(previousNative); Strings.refresh(); throw error as Error;
        }
        this.language = language;
        // Native 'default' changes resources only after a cold start. Keep this
        // launch's effective wire language until the UI actually follows it.
        if (language !== 'default') setClientLanguage(language);
        Strings.refresh();
        for (const view of this.views.values()) view.language = language;
        this.announceLanguage();
      });
    } catch (_) { this.error = applied ? 'cache_unavailable' : 'language_change_failed'; }
    finally { this.languageChanging = false; }
  }
  announceLanguage(): void {
    for (const owner of this.directoryOwners()) {
      if (owner.connected && !owner.demo) {
        try { owner.repo().transport.hello(nativeNotifications.features(!owner.detached && owner.alerts)); } catch (_) {}
      }
    }
  }
  async useEndpoint(config: EndpointConfig, name: string, admit?: () => boolean, route?: PairingSshRoute): Promise<void> {
    if (this.forgettingProfiles.has(config.hubId) || this.cleanupProfiles.has(config.hubId)) throw new Error('pending_action');
    if (!this.vault) throw new Error('storage_unavailable');
    if (this.pairingProfiles.size > 0) throw new Error('pending_action');
    if (admit && !admit()) throw new Error('request_updated');
    this.pairingProfiles.add(config.hubId); this.stopReconnect();
    try {
    this.retirePairing(config.hubId);
    this.retireViews(config.hubId);
    if (this.profile.id === config.hubId) this.ssh.stop();
    const epoch = this.forgetEpochs.get(config.hubId) || 0, vault = this.vault;
    const current = (): boolean => epoch === (this.forgetEpochs.get(config.hubId) || 0) && !this.forgettingProfiles.has(config.hubId) && (!admit || admit());
    let previous: string | null = null, written = false;
    let previousAccess: HubAccess | undefined;
    let settleRoute: ((committed: boolean) => Promise<void>) | undefined;
    const rollback = async (): Promise<void> => {
      let installedAccess: HubAccess | undefined;
      const installed = await vault.get(config.hubId, (value: HubAccess | undefined): void => { installedAccess = value; });
      if (installed !== config.token || (config.access ? !config.access.equals(installedAccess) : !!installedAccess)) return;
      if (previous && epoch === (this.forgetEpochs.get(config.hubId) || 0) && !this.forgettingProfiles.has(config.hubId)) await vault.put(config.hubId, previous, previousAccess);
      else await vault.remove(config.hubId);
    };
    await this.credentialChange(config.hubId, async (): Promise<void> => {
      if (!current()) throw new Error('request_updated');
      previous = await vault.get(config.hubId, (value: HubAccess | undefined): void => { previousAccess = value; }); if (!current()) throw new Error('request_updated');
      try {
        if (route) {
          this.local.allowHub(config.hubId);
          const state = this.captureViewPath(this.sshViewKey(config.hubId));
          settleRoute = await route.install(config.hubId, config.baseUrl, config.pin, state, current);
        }
        if (!current()) throw new Error('request_updated');
        await vault.put(config.hubId, config.token, config.access); written = true;
        if (!current()) throw new Error('request_updated');
      } catch (error) {
        try { if (written) await rollback(); } finally { if (settleRoute) await settleRoute(false); }
        throw error as Error;
      }
    });
    if (!current()) {
      try { if (written) await this.credentialChange(config.hubId, rollback); }
      finally { if (settleRoute) await settleRoute(false); }
      throw new Error('request_updated');
    }
    this.retireHubConnections(config.hubId);
    const profile = new HubProfile(config.hubId, name, config.baseUrl);
    profile.addresses = config.alternateUrls?.slice() || []; profile.hubId = config.hubId; profile.pin = config.pin; profile.manager = config.managerId;
    this.profiles = this.profiles.filter((item: HubProfile) => item.id !== profile.id).concat([profile]);
    if (settleRoute) await settleRoute(true);
    await this.selectProfile(profile);
    } finally {
      this.pairingProfiles.delete(config.hubId);
      if (!this.connected) this.scheduleReconnect();
    }
  }
  async selectProfile(profile: HubProfile, preserve: boolean = false, notification: boolean = false): Promise<void> {
    if (this.retired || this.forgettingProfiles.has(profile.id) || this.cleanupProfiles.has(profile.id)) throw new Error('pending_action');
    if (!this.detached && this.profile.id && (this.profile.id !== profile.id || this.profile.manager !== profile.manager)) this.retireViews();
    if (!notification && this.pendingNotification && (Wire.text(this.pendingNotification, 'profile') !== profile.id || Wire.text(this.pendingNotification, 'manager') !== profile.manager)) {
      this.pendingNotification = undefined; this.notificationEpoch++;
    }
    this.local.allowHub(profile.id); ChatAttachments.allowHub(profile.hubId || profile.id);
    this.ssh.stop(); this.stopReconnect(); this.cancelDraftTimer(); const keep = preserve && profile.id === this.profile.id && profile.manager === this.profile.manager;
    const saved = this.persistDraft(); this.closeTranscript(); this.releaseTerminal(); this.repository?.close(); this.repository = undefined;
    const generation = ++this.generation; this.selectionGeneration++; const selection = this.selected;
    this.sendAttempts.clear(); this.feedbackChanged();
    this.profile = profile; const scope = this.scope(); this.connected = false;
    this.clearPendingDecisions();
    this.unresolvedWrites = []; this.operationState = ''; this.mainId = ''; this.mainMachineId = ''; this.sidebarConfig = ''; this.features = []; this.rememberedHubModules = null; this.useHerdrTabName = false; this.agentsDisplaySupported = false; this.agentsDisplaySaving = false; this.agentsDisplayRevision++; this.nextSuggestionsValue = null; this.nextSuggestionsLoaded = false; this.nextSuggestionsSaving = false; this.nextSuggestionsError = ''; this.nextSuggestionsRevision++; this.managers = []; this.rosterDefaultId = '';
    this.namingAgentKeys = [];
    this.publishedImageLimits = {};
    this.demo = false; this.demoRepository = undefined; this.error = '';
    if (!keep) { this.sessionsReady = false; this.sessions = []; this.machines = []; this.conversations = []; this.todos = []; this.inbox = []; this.knowledge = []; this.messages = []; } this.selected = keep ? selection : new ResourceRow(); this.draftLoading = false;
    if (!keep && !this.detached) { this.directoryReady = false; this.directoryLoading = false; this.directoryUnavailable = 0; this.publishDirectory(); }
    if (!keep) { this.draft = ''; this.sourceContext = ''; this.draftRevision = ''; this.localMessages = []; this.renderedMessages = []; this.echoedMessages.clear(); }
    await saved; if (!this.owns(generation, scope)) return;
    try { this.rememberedHubModules = parseStoredHubModules(await this.local.get(this.hubModulesKey(profile.id))); } catch (_) { this.rememberedHubModules = null; }
    let cachedDisplay = '';
    try { cachedDisplay = await this.local.get(this.agentsDisplayKey(profile.id)); } catch (_) {}
    if (!this.owns(generation, scope)) return;
    if (cachedDisplay) {
      try {
        const parsedDisplay = Wire.parse(cachedDisplay);
        if (typeof parsedDisplay['use_herdr_tab_name'] === 'boolean') {
          this.useHerdrTabName = parsedDisplay['use_herdr_tab_name'] === true;
          this.agentsDisplaySupported = true;
        }
      } catch (_) {}
    }
    await this.saveProfiles(); if (!this.owns(generation, scope) || !this.foreground) return;
    let access: HubAccess | undefined;
    const token = await this.vault?.get(profile.id, (value: HubAccess | undefined): void => { access = value; });
    if (!this.owns(generation, scope)) return;
    if (!token) { this.error = 'welcome'; return; }
    if (profile.manager) await this.restoreSnapshot(generation, scope);
    let proxy: SshProxyLease | undefined;
    try {
      await this.ssh.bind(this, (): boolean => this.foreground && !this.retired && !this.forgettingProfiles.has(profile.id));
      if (!this.owns(generation, scope) || !this.foreground) return;
      const current = (): boolean => this.owns(generation, scope) && this.foreground;
      const direct = (required: boolean): Promise<string> => Pairing.reachable(profile.baseUrl, profile.addresses,
        profile.hubId, profile.pin, current, access, required);
      const config = this.ssh.configuration, policy = config?.enabled ? config.routePolicy : 'legacy';
      let address = profile.baseUrl;
      if (policy === 'fallback') {
        try { address = await direct(true); }
        catch (error) {
          if (!current()) return;
          proxy = await this.ssh.acquire(current);
          if (!proxy) throw error as Error;
          this.ssh.watchDirect((): Promise<string> => direct(true), (): void => {
            if (current()) this.selectProfile(profile,true).catch((failure: Error) => this.failure(failure));
          });
        }
      } else {
        try { proxy = await this.ssh.acquire(); }
        catch (error) { if (policy !== 'always' || !current()) throw error as Error; }
        if (!proxy) address = await direct(policy === 'always');
      }
      if (!this.owns(generation, scope) || !this.foreground) return;
      if (!proxy && (policy === 'always' || policy === 'fallback')) this.ssh.usingDirectAddress();
      if (address !== profile.baseUrl) { profile.addresses = Array.from(new Set<string>([profile.baseUrl].concat(profile.addresses))).filter((value: string) => value !== address && value.startsWith('https://')); profile.baseUrl = address; await this.saveProfiles(); }
    } catch (error) { if (this.owns(generation, scope)) { this.failure(error as Error); this.scheduleReconnect(); } return; }
    if (!this.owns(generation, scope) || !this.foreground) return;
    let defaultOwner = '';
    const probe = new HubTransport(new EndpointConfig(profile.baseUrl, profile.hubId, token, profile.pin, '', [], proxy, access));
    try {
      const roster = Wire.parse(await probe.request('/api/v1/managers'));
      if (!this.owns(generation, scope)) return;
      defaultOwner = Wire.text(roster, 'default'); this.rosterDefaultId = defaultOwner;
      const managerId = resolveManagerScope(profile.manager, defaultOwner, Wire.rows(roster, 'managers').map((item: Record<string, Object>) => Wire.text(item, 'id')));
      if (managerId !== profile.manager) {
        const canonical = new HubProfile(profile.id, profile.name, profile.baseUrl);
        canonical.addresses = profile.addresses.slice(); canonical.hubId = profile.hubId; canonical.pin = profile.pin; canonical.manager = managerId;
        this.profiles = this.profiles.map((item: HubProfile) => item.id === canonical.id ? canonical : item);
        await this.selectProfile(canonical); return;
      }
      this.managers = Wire.rows(roster, 'managers').map((item: Record<string,Object>) => ResourceRow.from(item,'manager'));
    } catch (error) {
      if (!this.owns(generation, scope)) return;
      // A hub without manager support has one legacy scope. Never downgrade a named manager.
      if (!(error instanceof HttpFailure && error.status === 404 && !profile.manager)) { this.failure(error as Error); this.scheduleReconnect(); return; }
    } finally { probe.close(); }
    if (!profile.manager) await this.restoreSnapshot(generation, scope);
    if (!this.owns(generation, scope) || !this.foreground) return;
    const config = new EndpointConfig(profile.baseUrl, profile.hubId, token, profile.pin, profile.manager, [], proxy, access);
    this.repository = new HubRepository(config, this.local, () => { if (this.owns(generation, scope)) this.unresolvedWrites = this.repository?.mutations?.blocked() || []; }, defaultOwner);
    await this.repository.mutations?.load(); if (!this.owns(generation, scope) || !this.foreground) return; this.unresolvedWrites = this.repository.mutations?.blocked() || [];
    await this.repository.transport.connect((payload: string) => {
      if (this.owns(generation, scope)) this.event(payload);
    }, (state: string) => {
      if (!this.owns(generation, scope)) return;
      this.connected = state === 'ready' || state === 'online';
      if (this.connected) { this.reconnectAttempt = 0; this.stopReconnect(); }
      if (state === 'offline' || state === 'closed') { this.helloGeneration = -1; this.clearPendingDecisions(); this.releaseTerminal(); this.error = 'offline'; this.scheduleReconnect(); }
      else if (this.connected && this.isChat() && (this.chatVisible || this.decisionsVisible)) {
        const row = this.selected;
        if (this.chatVisible) this.loadChat(row).catch((error: Error) => this.failure(error));
        else this.refreshDecisions().catch((error: Error) => this.failure(error));
      }
      this.publishDirectory();
      if (!this.detached) nativeLiveView.syncFromModel(this).catch(() => {});
    }).catch((error: Error) => { if (this.owns(generation, scope)) this.failure(error); });
    if (this.owns(generation, scope)) await this.flushNotification();
  }
  private async restoreSnapshot(generation: number, scope: string): Promise<void> {
    try {
      const owner = this.heldOwner(), heldRevision = owner.heldRevision(scope);
      const cached = await new ChatDraftStore(this.local).readSessionSnapshot(scope);
      if (!this.owns(generation, scope) || heldRevision !== owner.heldRevision(scope) || !cached) return;
      const snapshot = Wire.record(JSON.parse(cached) as Object);
      const restored = owner.heldReceipts.filter((receipt: HeldActionReceipt) => receipt.scope !== scope);
      for (const saved of Wire.rows(snapshot, 'heldActions')) {
        const target = Wire.text(saved, 'target'), expected = new HeldAgentMessage();
        expected.id = Wire.text(saved, 'id'); if (!target || !expected.id) continue;
        expected.state = Wire.text(saved, 'state'); expected.line = Wire.text(saved, 'line'); expected.deleteOnly = Wire.bool(saved, 'deleteOnly');
        restored.push({ scope: scope, pairing: this.pairingRevision(), target: target, expected: expected, deleted: Wire.bool(saved, 'deleted') });
      }
      owner.heldReceipts = restored;
      this.sessionsReady = Wire.has(snapshot, 'sessions');
      this.sessions = Wire.rows(snapshot, 'sessions').map((data: Record<string, Object>) => this.cachedRow(data, 'session'));
      this.sidebarConfig = Wire.text(snapshot, 'sidebarConfig');
      if (this.sidebarConfig) {
        const config=Wire.parse(this.sidebarConfig);
        this.mainId=Wire.text(config,'main'); this.mainMachineId=Wire.text(config,'main_machine_id');
      }
      this.conversations = Wire.rows(snapshot, 'conversations').map((data: Record<string, Object>) => this.cachedRow(data, 'conversation')); this.publishDirectory();
    } catch (_) { if (this.owns(generation, scope)) this.error = 'cache_unavailable'; }
  }
  listedManagers(): ResourceRow[] {
    return this.managers.filter((row: ResourceRow) => !!row.id && !Wire.bool(row.data(), 'hidden'));
  }
  async selectListedManager(id: string, sourceScope: string): Promise<void> {
    if (sourceScope !== this.scope() || this.profile.manager === id) return;
    const row = this.listedManagers().find((item: ResourceRow) => item.id === id);
    if (!row || row.isInstallingManager()) return;
    const group = this.profile.id;
    try { await this.selectManager(id); }
    catch (error) {
      if (this.profile.id === group && (this.scope() === sourceScope || this.profile.manager === id)) this.reportError(error as Error);
    }
  }
  async selectManager(id: string): Promise<void> {
    if (this.demo && id === this.profile.manager) return;
    const profile = new HubProfile(this.profile.id, this.profile.name, this.profile.baseUrl);
    profile.addresses = this.profile.addresses.slice(); profile.hubId = this.profile.hubId; profile.pin = this.profile.pin; profile.manager = id;
    this.profiles = this.profiles.map((item: HubProfile) => item.id === profile.id ? profile : item);
    await this.selectProfile(profile);
  }
  async forgetProfile(id: string): Promise<void> {
    if (this.directoryParent) return this.directoryParent.forgetProfile(id);
    let profile = this.profiles.find((item: HubProfile) => item.id === id);
    if (!profile && this.cleanupProfiles.has(id)) { profile = new HubProfile(id); profile.hubId = this.cleanupProfiles.get(id) || id; }
    if (!profile || this.forgettingProfiles.has(id)) return;
    this.cleanupProfiles.set(id, profile.hubId || id); this.pendingCleanupCount = this.cleanupProfiles.size;
    this.forgettingProfiles.add(id); AssistantViewModel.forgetProfile(id); this.retireViews(id);
    this.retirePairing(id);
    const belongsToHub = (scope: string): boolean => (JSON.parse(scope) as string[])[0] === id;
    this.heldReceipts = this.heldReceipts.filter((receipt: HeldActionReceipt) => !belongsToHub(receipt.scope));
    for (const scope of this.heldRevisions.keys()) if (belongsToHub(scope)) this.heldRevisions.delete(scope);
    this.forgetEpochs.set(id, (this.forgetEpochs.get(id) || 0) + 1);
    if (this.pendingNotification && Wire.text(this.pendingNotification, 'profile') === id) { this.pendingNotification = undefined; this.notificationEpoch++; }
    try {
    if (this.profile.id === id) {
      this.ssh.stop();
      this.stopReconnect(); this.cancelDraftTimer(); this.closeTranscript(); this.releaseTerminal();
      this.repository?.close(); this.repository = undefined; this.generation++; this.selectionGeneration++;
      this.profile = new HubProfile(); this.connected = false; this.selected = new ResourceRow();
      this.sendAttempts.clear(); this.feedbackChanged();
      this.sessionsReady = false; this.sessions = []; this.conversations = []; this.machines = []; this.managers = []; this.todos = []; this.knowledge = []; this.inbox = [];
      this.clearPendingDecisions();
      this.messages = []; this.draft = ''; this.sourceContext = ''; this.draftRevision = ''; this.localMessages = []; this.renderedMessages = []; this.echoedMessages.clear(); this.detail = ''; this.reply = ''; this.mainId = ''; this.mainMachineId = ''; this.sidebarConfig = '';
      this.unresolvedWrites = []; this.operationState = ''; this.notificationTarget = ''; this.pendingXiaoYiRoute = ''; this.draftLoading = false; this.features = []; this.rememberedHubModules = null; this.useHerdrTabName = false; this.agentsDisplaySupported = false; this.agentsDisplaySaving = false; this.agentsDisplayRevision++; this.nextSuggestionsValue = null; this.nextSuggestionsLoaded = false; this.nextSuggestionsSaving = false; this.nextSuggestionsError = ''; this.nextSuggestionsRevision++; this.error = '';
      this.namingAgentKeys = [];
      this.publishedImageLimits = {};
    }
    this.publishDirectory();
    try { await this.saveCleanup(); } catch (_) { this.error = 'cleanup_incomplete'; throw new Error('cleanup_incomplete'); }
    const forgettingSsh = this.ssh.forget(id);
    const forgettingRecordings = this.context ? KnowledgeRecording.forgetProfile(id, this.context) : Promise.resolve();
    const forgettingLook = deviceLook.forgetHub(id, profile.hubId || id);
    // Retire writes before awaiting credential removal so late callbacks cannot recreate cached content.
    const purging = this.local.purgeHub(id);
    this.forgottenProfile = id; this.forgottenEpoch++;
    MutationStore.forgetHub(this.local, id);
    const cleanup = await Promise.allSettled([forgettingSsh, forgettingRecordings, forgettingLook, purging]);
    let cleanupFailed = cleanup.some((result: PromiseSettledResult<void>): boolean => result.status === 'rejected');
    if (this.context) { try { await ChatAttachments.forgetHub(this.context, profile.hubId || id); } catch (_) { cleanupFailed = true; } }
    if (this.context) { try { clearImagePreviews(this.context, profile.hubId || id); } catch (_) { cleanupFailed = true; } }
    try { await nativeNotifications.clear(id); } catch (_) { cleanupFailed = true; }
    if (!this.detached) { nativeLiveView.stopNow(id).catch(() => {}); }
    if (this.context) {
      for (const hook of ClientModel.forgetHooks) {
        try { await hook(id, this.context); } catch (_) {}
      }
    }
    try { await this.credentialChange(id, async (): Promise<void> => { await this.vault?.remove(id); }); } catch (_) { cleanupFailed = true; }
    this.profiles = this.profiles.filter((item: HubProfile) => item.id !== id); this.publishDirectory();
    try { await this.saveProfiles(); } catch (_) { cleanupFailed = true; }
    if (cleanupFailed) { this.error = 'cleanup_incomplete'; throw new Error('cleanup_incomplete'); }
    this.cleanupProfiles.delete(id);
    try { await this.saveCleanup(); }
    catch (_) { this.cleanupProfiles.set(id, profile.hubId || id); this.pendingCleanupCount = this.cleanupProfiles.size; this.error = 'cleanup_incomplete'; throw new Error('cleanup_incomplete'); }
    if (this.cleanupProfiles.size === 0 && this.error === 'cleanup_incomplete') this.error = '';
    } finally { this.forgettingProfiles.delete(id); }
  }
  private event(payload: string): void {
    try {
      const data = Wire.parse(payload), type = Wire.text(data, 'type');
      if (type === 'hello') {
        this.helloGeneration = -1;
        const hubId = Wire.text(data, 'hub_id');
        if (hubId && hubId !== this.profile.hubId) { this.repository?.close(); this.error = 'identity_changed'; return; }
        if (hubId && hubId === this.profile.hubId) {
          this.helloHub = hubId; this.helloScope = this.scope(); this.helloGeneration = this.generation;
          this.helloVersion = Wire.text(data, 'server_version'); this.helloFeatures = Wire.strings(data, 'features');
        }
      }
      this.workspace?.onEvent(data);
      if (type === 'tool_call') this.deviceEvent?.(payload);
      for (const listener of this.eventListeners) { try { listener(payload, type); } catch (_) {} }
      if (type === 'hello') {
        this.publishedImageLimits = Wire.object(data, 'image_limits');
        this.features = Wire.strings(data, 'features'); this.connected = true; this.error = '';
        if (this.features.length > 0 && this.profile.id) {
          const published = extractPublishedModules(this.features);
          this.rememberedHubModules = published;
          this.local.put(this.hubModulesKey(this.profile.id), serializeStoredHubModules(published)).catch(() => {});
        }
        this.repository?.transport.hello(nativeNotifications.features(!this.detached && this.alerts));
        const generation = this.generation, scope = this.scope();
        this.reconcileWrites().then(async (): Promise<void> => { if (this.owns(generation, scope)) await this.refresh(); }).then(async (): Promise<void> => { if (this.owns(generation, scope)) await this.flushNotification(); }).catch((error: Error) => { if (this.owns(generation, scope)) this.failure(error); });
      } else if (type === 'notify') { if (!this.detached) nativeNotifications.receive(this, data); }
      else if (type === 'fleet') { if (!this.detached) nativeLiveView.receiveFleetEvent(this, data).catch(() => {}); }
      else if (type === 'navigation') {
        if (Wire.has(data, 'pages') || Wire.has(data, 'hidden') || Wire.has(data, 'hidden_pages')) this.applyNavigationConfig(data);
        else if (this.repository && !this.demo) this.refreshNavigation(this.repository, this.generation, this.scope()).catch(() => {});
      }
      else if (type === 'items' || type === 'items_replace') { this.transcript?.receive(data); } else if (type === 'terminal') {
        if (Wire.text(data, 'machine_id') === this.selected.machine && Wire.text(data, 'session_id') === this.selected.session)
          this.terminalText = (this.terminalText + Wire.text(data, 'data')).slice(-200000);
      } else if (type === 'assistant_starred') {
        this.starredRevision++;
      } else if (['interaction', 'interactions', 'interaction_resolved', 'session', 'sessions', 'sessions_delta', 'item', 'items', 'session_delta', 'todo', 'inbox', 'task_plan', 'assistant_feed', 'managers', 'machines'].includes(type)) {
        if (type === 'inbox') this.inboxRead.change++;
        if (type === 'todo') this.todoRead.change++;
        this.queueRefresh();
      }
    } catch (_) { this.error = 'failed'; }
  }
  private queueRefresh(): void {
    if (this.feedTimer !== -1) clearTimeout(this.feedTimer);
    const generation = this.generation, scope = this.scope();
    this.feedTimer = setTimeout(() => {
      this.feedTimer = -1;
      // A push may arrive after an in-flight GET already read the previous row.
      Promise.resolve(this.refreshTask).then(async (): Promise<void> => {
        if (this.owns(generation, scope)) await this.refresh();
      }).catch((error: Error) => { if (this.owns(generation, scope)) this.failure(error); });
    }, 100);
  }
  async refresh(): Promise<void> {
    const owner = JSON.stringify([this.generation, this.scope()]);
    if (this.refreshTask && this.refreshOwner === owner) return this.refreshTask;
    const task = this.refreshCurrent(); this.refreshTask = task; this.refreshOwner = owner;
    try { await task; } finally { if (this.refreshTask === task) this.refreshTask = undefined; }
  }
  private async refreshCurrent(): Promise<void> {
    if (this.demo && this.demoRepository) { this.refreshDemo(); if (this.isChat() && this.chatVisible) await this.loadChat(this.selected); return; }
    if (!this.connected || !this.repository) return;
    const generation = this.generation, scope = this.scope(), repo = this.repository, owner = this.heldOwner(), heldRevision = owner.heldRevision(scope);
    this.busy = true;
    try {
      const sessions = await repo.sessions(), assistant = await repo.call('/api/v1/assistant');
      const machines = await repo.machines();
      let managers: ResourceRow[] = [];
      let rosterDefault = this.rosterDefaultId;
      if (this.managers.length || this.features.includes('sessions_manager') || this.features.includes('managers')) {
        try { managers = await repo.list('/api/v1/managers', 'managers', 'manager', false, (roster: Record<string,Object>) => { rosterDefault = Wire.text(roster,'default'); }); }
        catch (error) { if (!(error instanceof HttpFailure) || ![404,501].includes(error.status)) throw error as Error; }
      }
      if (!this.owns(generation, scope)) return;
      if (heldRevision !== owner.heldRevision(scope)) { this.queueRefresh(); return; }
      sessions.forEach((row: ResourceRow) => row.group = this.profile.id); machines.forEach((row: ResourceRow) => row.group = this.profile.id);
      this.sessions = sessions; this.sessionsReady = true; this.machines = machines; this.managers = managers; this.rosterDefaultId = rosterDefault;
      this.updateSendFeedbackSessions(sessions);
      this.notifyStatusHooks();
      if (owner.heldReceipts.length) owner.heldReceipts = owner.heldReceipts.filter((receipt: HeldActionReceipt) => {
        if (receipt.scope !== scope) return true;
        const row = sessions.find((item: ResourceRow) => this.target(item) === receipt.target);
        if (receipt.pairing !== this.pairingRevision()) return false;
        // The Hub can omit an entire computer while its manager still holds this operation.
        if (!row) return true;
        if (receipt.deleted) return this.removeHeldMessage(row, receipt.expected.id);
        // The Hub can return the same cached failure after the manager has already started checking.
        return !!this.matchingHeldInstruction(receipt.expected, row);
      });
      this.publishDirectory();
      if (!this.detached) nativeLiveView.syncFromModel(this).catch(() => {});
      if (this.selected.kind === 'session') {
        const selected = sessions.find((item: ResourceRow) => item.id === this.selected.id && item.machine === this.selected.machine);
        if (selected) this.selected = selected;
        else { this.selected.raw = JSON.stringify(Wire.payload(['read_only'], [true])); this.selected.state = 'unknown'; this.clearPendingDecisions(); }
        this.reconcileMessageCopies();
      }
      this.acceptSidebarConfig(assistant);
      this.mainId = Wire.text(assistant, 'main'); this.mainMachineId = Wire.text(assistant, 'main_machine_id', Wire.text(assistant, 'machine_id'));
      this.conversations = Wire.rows(assistant, 'conversations').map((data: Record<string, Object>) => {
        const row = ResourceRow.from(data, 'conversation'); row.manager = this.profile.manager;
        if (!row.machine) row.machine = Wire.text(assistant, 'machine_id'); if (!row.session) row.session = row.id;
        return row;
      });
      const heldActions = owner.heldReceipts.filter((receipt: HeldActionReceipt) => receipt.scope === scope)
        .map((receipt: HeldActionReceipt) => this.cachedHeldAction(receipt));
      try { await new ChatDraftStore(this.local).saveSessionSnapshot(scope, sessions, this.conversations, heldActions,
        (): boolean => this.owns(generation, scope) && owner.heldRevision(scope) === heldRevision, this.sidebarConfig); }
      catch (_) { if (this.owns(generation, scope)) this.error = 'cache_unavailable'; }
      if (this.isChat() && this.chatVisible) await this.loadChat(this.selected);
      else if (this.isChat() && this.decisionsVisible) await this.refreshDecisions();
      else this.readInteractionSnapshot().catch(() => {});
      this.refreshPhoneList('todo', repo, generation, scope, false).catch(() => {});
      if (this.section === 2) await this.refreshPhoneList('inbox', repo, generation, scope, true);
      else if (!this.detached) this.refreshPhoneList('inbox', repo, generation, scope, false).catch(() => {});
      this.refreshAgentsDisplay(repo, generation, scope).catch(() => {});
      this.refreshNavigation(repo, generation, scope).catch(() => {});
    } catch (error) { if (this.owns(generation, scope)) this.failure(error as Error); }
    finally { if (this.owns(generation, scope)) { this.busy = false; this.publishDirectory(); } }
  }
  private syncViewsAgentsDisplay(profileId: string, supported: boolean, enabled: boolean): void {
    for (const view of this.views.values()) {
      if (view.profile.id === profileId && !view.retired) {
        view.agentsDisplaySupported = supported;
        view.useHerdrTabName = enabled;
      }
    }
  }
  async refreshNavigation(repo: HubRepository, generation: number, scope: string): Promise<void> {
    if (this.demo) return;
    try {
      const result = await repo.call('/api/v1/navigation');
      if (!this.owns(generation, scope)) return;
      this.applyNavigationConfig(result);
    } catch (_) { /* Older hubs omit /api/v1/navigation; leave existing page visibility untouched. */ }
  }
  async refreshAgentsDisplay(repo: HubRepository, generation: number, scope: string): Promise<void> {
    if (this.demo || this.agentsDisplaySaving) return;
    const rev = this.agentsDisplayRevision;
    try {
      const result = await repo.call('/api/v1/agents/display-settings');
      if (!this.owns(generation, scope) || rev !== this.agentsDisplayRevision || this.agentsDisplaySaving) return;
      if (typeof result['use_herdr_tab_name'] === 'boolean') {
        const enabled = result['use_herdr_tab_name'] === true;
        this.agentsDisplaySupported = true;
        this.useHerdrTabName = enabled;
        this.syncViewsAgentsDisplay(this.profile.id, true, enabled);
        this.local.put(this.agentsDisplayKey(this.profile.id), JSON.stringify(Wire.payload(['use_herdr_tab_name'], [enabled]))).catch(() => {});
      } else {
        this.agentsDisplaySupported = false;
        this.useHerdrTabName = false;
        this.syncViewsAgentsDisplay(this.profile.id, false, false);
      }
    } catch (error) {
      if (error instanceof HttpFailure && [404, 501].includes(error.status) && this.owns(generation, scope) && rev === this.agentsDisplayRevision) {
        this.agentsDisplaySupported = false;
        this.useHerdrTabName = false;
        this.syncViewsAgentsDisplay(this.profile.id, false, false);
      }
    }
  }
  async setUseHerdrTabName(enabled: boolean): Promise<boolean> {
    if (this.demo || !this.connected || !this.repository || this.agentsDisplaySaving) return false;
    const generation = this.generation, scope = this.scope(), repo = this.repository, profileId = this.profile.id;
    const previous = this.useHerdrTabName;
    const rev = ++this.agentsDisplayRevision;
    this.agentsDisplaySaving = true;
    this.useHerdrTabName = enabled;
    this.syncViewsAgentsDisplay(profileId, true, enabled);
    try {
      const result = await repo.call('/api/v1/agents/display-settings', 'PUT', Wire.payload(['use_herdr_tab_name'], [enabled]));
      if (!this.owns(generation, scope) || this.repository !== repo || rev !== this.agentsDisplayRevision) return false;
      const acknowledged = typeof result['use_herdr_tab_name'] === 'boolean' ? result['use_herdr_tab_name'] === true : enabled;
      this.agentsDisplaySupported = true;
      this.useHerdrTabName = acknowledged;
      this.syncViewsAgentsDisplay(profileId, true, acknowledged);
      await this.local.put(this.agentsDisplayKey(profileId), JSON.stringify(Wire.payload(['use_herdr_tab_name'], [acknowledged]))).catch(() => {});
      return true;
    } catch (error) {
      if (this.owns(generation, scope) && this.repository === repo && rev === this.agentsDisplayRevision) {
        this.useHerdrTabName = previous;
        this.syncViewsAgentsDisplay(profileId, true, previous);
        this.failure(error as Error);
      }
      return false;
    } finally {
      if (this.owns(generation, scope) && rev === this.agentsDisplayRevision) this.agentsDisplaySaving = false;
    }
  }
  syncNextSuggestions(assistant: Record<string, Object>): void {
    const parsed = parseAssistantNextSuggestions(assistant);
    if (parsed !== null) {
      this.nextSuggestionsValue = parsed;
      this.nextSuggestionsLoaded = true;
      if (this.demo) this.refreshDemo();
    }
    const parent = this.directoryParent;
    if (parsed !== null && parent && parent.scope() === this.scope() && parent.profile.hubId === this.profile.hubId) {
      parent.nextSuggestionsValue = parsed;
      parent.nextSuggestionsLoaded = true;
      if (parent.demo) parent.refreshDemo();
    }
  }
  async loadNextSuggestions(): Promise<void> {
    if ((!this.connected && !this.demo) || !this.repository || this.nextSuggestionsSaving) return;
    const generation = this.generation, scope = this.scope(), repo = this.repository;
    const rev = ++this.nextSuggestionsRevision;
    this.nextSuggestionsError = '';
    try {
      const result = await repo.call('/api/v1/assistant');
      if (!this.owns(generation, scope) || this.repository !== repo || rev !== this.nextSuggestionsRevision) return;
      this.nextSuggestionsValue = parseAssistantNextSuggestions(result);
      this.nextSuggestionsLoaded = true;
    } catch (error) {
      if (this.owns(generation, scope) && this.repository === repo && rev === this.nextSuggestionsRevision) {
        this.nextSuggestionsError = error instanceof Error ? error.message : 'failed';
      }
    }
  }
  async setNextSuggestions(enabled: boolean): Promise<boolean> {
    if ((!this.connected && !this.demo) || !this.repository || this.nextSuggestionsValue === null || this.nextSuggestionsSaving) return false;
    const previous = this.nextSuggestionsValue;
    if (previous === enabled) return true;
    const generation = this.generation, scope = this.scope(), repo = this.repository;
    const rev = ++this.nextSuggestionsRevision;
    this.nextSuggestionsSaving = true;
    this.nextSuggestionsError = '';
    this.nextSuggestionsValue = enabled;
    try {
      const result = await repo.call('/api/v1/assistant', 'PUT', buildNextSuggestionsPayload(enabled));
      if (!this.owns(generation, scope) || this.repository !== repo || rev !== this.nextSuggestionsRevision) return false;
      const authoritative = resolveAuthoritativeNextSuggestions(result, enabled);
      this.nextSuggestionsValue = authoritative;
      this.nextSuggestionsLoaded = true;
      if (this.demo) this.refreshDemo();
      return authoritative === enabled;
    } catch (error) {
      if (this.owns(generation, scope) && this.repository === repo && rev === this.nextSuggestionsRevision) {
        this.nextSuggestionsValue = previous;
        this.nextSuggestionsError = error instanceof Error ? error.message : 'failed';
        this.failure(error as Error);
      }
      return false;
    } finally {
      if (this.owns(generation, scope) && rev === this.nextSuggestionsRevision) this.nextSuggestionsSaving = false;
    }
  }
  private async refreshPhoneList(kind: string, repo: HubRepository, generation: number, scope: string, force: boolean): Promise<void> {
    const state = kind === 'todo' ? this.todoRead : this.inboxRead;
    const change = state.change, key = JSON.stringify([generation, scope, change]);
    if (key === state.pendingKey && state.pending) return state.pending;
    if (!force && key === state.snapshot) return;
    state.pendingKey = key;
    const read = kind === 'todo' ? repo.todos() : repo.inbox();
    state.pending = read.then((rows: ResourceRow[]) => {
      if (!this.owns(generation, scope) || state.change !== change) return;
      if (kind === 'todo') this.todos = rows;
      else this.inbox = rows;
      state.snapshot = key;
    }).finally(() => {
      if (state.pendingKey === key) { state.pending = undefined; state.pendingKey = ''; }
    });
    return state.pending;
  }
  phoneTodoBadge(): number {
    if (!this.connected) return 0;
    return this.todos.filter((row: ResourceRow): boolean =>
      (!row.manager || row.manager === this.profile.manager) && !row.isDone() && !Wire.bool(row.data(), 'quiet')).length;
  }
  async openMain(): Promise<ResourceRow> {
    const generation = this.generation, scope = this.scope(), repository = this.repository;
    await this.refresh();
    if (!this.owns(generation, scope) || this.repository !== repository) throw new Error('request_updated');
    let row = this.conversations.find((item: ResourceRow) => item.id === this.mainId && (!this.mainMachineId || item.machine === this.mainMachineId));
    if (!row) {
      row = await this.repo().newConversation(true, util.generateRandomUUID());
      if (!this.owns(generation, scope) || this.repository !== repository) throw new Error('request_updated');
      await this.refresh();
    }
    if (!this.owns(generation, scope) || this.repository !== repository) throw new Error('request_updated');
    if (!row.title) row.title = tr('main_chat'); await this.select(row);
    if (!this.owns(generation, scope) || this.selected.key() !== row.key()) throw new Error('request_updated'); return row;
  }
  async newConversation(): Promise<ResourceRow> {
    const generation = this.generation, scope = this.scope(), repository = this.repo();
    const row = await repository.newConversation(false, util.generateRandomUUID());
    if (!this.owns(generation, scope) || this.repository !== repository) throw new Error('request_updated');
    if (!row.title) row.title = tr('new_chat'); row.group = this.profile.id; await this.refresh();
    if (!this.owns(generation, scope) || this.repository !== repository) throw new Error('request_updated');
    await this.select(row); if (!this.owns(generation, scope) || this.selected.key() !== row.key()) throw new Error('request_updated'); return row;
  }
  async openNotification(target: Record<string, Object>): Promise<void> {
    const id = Wire.text(target, 'profile'), manager = Wire.text(target, 'manager'), machine = Wire.text(target, 'machine'), session = Wire.text(target, 'session');
    if ([id, manager, machine, session].some((value: string) => value.length > 256) || !id || !machine || !session) return;
    const isDemoTarget = this.demo && id === this.profile.id;
    const saved = isDemoTarget ? this.profile : this.profiles.find((item: HubProfile) => item.id === id);
    if (!saved) return;
    this.pendingNotification = Wire.payload(['profile', 'manager', 'machine', 'session'], [id, manager, machine, session]);
    this.notificationEpoch++;
    if (!this.foreground) return;
    if (!isDemoTarget && (this.profile.id !== id || this.profile.manager !== manager || !this.connected)) {
      const profile = new HubProfile(saved.id, saved.name, saved.baseUrl); profile.addresses = saved.addresses.slice(); profile.hubId = saved.hubId; profile.pin = saved.pin; profile.manager = manager;
      await this.selectProfile(profile, this.profile.id === id && this.profile.manager === manager, true);
    }
    await this.flushNotification();
  }
  private async flushNotification(): Promise<void> {
    const target = this.pendingNotification, epoch = this.notificationEpoch;
    if (!target || !this.foreground || !this.connected) return;
    const id = Wire.text(target, 'profile'), manager = Wire.text(target, 'manager'), machine = Wire.text(target, 'machine'), session = Wire.text(target, 'session');
    if (this.profile.id !== id || this.profile.manager !== manager || this.forgettingProfiles.has(id)) return;
    const generation = this.generation, scope = this.scope(); this.error = '';
    await this.refresh();
    if (epoch !== this.notificationEpoch || target !== this.pendingNotification || !this.owns(generation, scope) || !this.foreground || !this.connected || this.error) return;
    const row = this.conversations.concat(this.sessions).find((item: ResourceRow) => item.machine === machine && (item.session || item.id) === session);
    this.pendingNotification = undefined;
    if (row) this.notificationTarget = JSON.stringify(row); else this.error = 'request_updated';
  }
  private cachedRow(data: Record<string, Object>, kind: string): ResourceRow {
    const row = ResourceRow.from(data, kind); row.group = this.profile.id; row.raw = Wire.text(data, 'raw', '{}'); row.session = Wire.text(data, 'session', row.id); return row;
  }
  private cancelDraftTimer(): void { if (this.draftTimer !== -1) clearTimeout(this.draftTimer); this.draftTimer = -1; }
  sendFeedbackPhase(): string {
    if (!this.sendFeedbackRevision) return '';
    const current = this.sendAttempts.get(this.target());
    const phase = current?.visible(Date.now()) || '';
    // After its own echo, actual running work has its own status line.
    if (phase === 'processing' && current && current.echoSequence >= 0 && this.renderedMessages.some((m: ChatMessage) => m.role === 'work' && m.running)) return '';
    return phase;
  }
  isSendingMessage(id: string): boolean { return this.sendFeedbackPhase() === 'sending' && this.sendAttempts.get(this.target())?.id === id; }
  sendFeedbackWorkHeader(): boolean {
    return this.sendFeedbackPhase() === 'processing' && (this.sendAttempts.get(this.target())?.echoSequence ?? -1) >= 0;
  }
  private feedbackChanged(): void {
    this.sendFeedbackRevision++;
    const current = this.sendAttempts.get(this.target()), now = Date.now();
    const deadline = this.isForegroundChat() && current?.visible(now) === 'processing' ? current.acceptedAt + ChatSendFeedback.acceptedWindow : 0;
    if (deadline === this.feedbackDeadline) return;
    if (this.feedbackTimer !== -1) clearTimeout(this.feedbackTimer);
    this.feedbackTimer = -1; this.feedbackDeadline = deadline;
    if (deadline) this.feedbackTimer = setTimeout(() => { this.feedbackTimer = -1; this.feedbackDeadline = 0; this.feedbackChanged(); }, Math.max(1, deadline-now));
  }
  private makeSendFeedback(row: ResourceRow, entry: ChatOutboxEntry): ChatSendFeedback {
    const current = this.sessionRow(row), data = current.data();
    return new ChatSendFeedback(entry.id, row.machine, row.session || row.id, entry.afterSequence, entry.at,
      Wire.text(data,'status',current.state), Wire.text(data,'failure_reason'), Wire.text(data,'state_since'));
  }
  private updateSendFeedbackSessions(rows: ResourceRow[]): void {
    if (!this.sendAttempts.size) return;
    const now = Date.now();
    for (const target of this.sendAttempts.keys()) {
      const attempt = this.sendAttempts.get(target); if (!attempt) continue;
      if (!attempt.visible(now) && now - Math.max(attempt.acceptedAt,attempt.startedAt) >= ChatSendFeedback.acceptedWindow) {
        this.sendAttempts.delete(target); continue;
      }
      const row = rows.find((r: ResourceRow) => r.machine === attempt.machine && (r.session || r.id) === attempt.session);
      if (row) {
        const data = row.data();
        const wasProcessing = attempt.visible(now) === 'processing';
        attempt.observeSession(Wire.text(data,'status',row.state), Wire.text(data,'failure_reason'), Wire.text(data,'state_since'));
        if (wasProcessing && !attempt.visible(now)) this.endSendFeedback(this.scope(),target,attempt);
      }
    }
    this.feedbackChanged();
  }
  private async endSendFeedback(scope: string, target: string, attempt: ChatSendFeedback): Promise<void> {
    attempt.finish(); this.feedbackChanged();
    try { await new ChatDraftStore(this.local).finishFeedback(scope,attempt.id); } catch (_) {
      if (this.scope() === scope && this.target() === target) this.error = 'cache_unavailable';
    }
  }
  async select(row: ResourceRow): Promise<void> {
    if (row.group && row.group !== this.profile.id || row.manager && row.manager !== this.profile.manager) throw new Error('request_updated');
    const generation = this.generation, scope = this.scope(), selection = ++this.selectionGeneration;
    this.cancelDraftTimer(); await this.persistDraft(); if (!this.owns(generation, scope) || selection !== this.selectionGeneration) return;
    this.closeTranscript(); this.releaseTerminal(); this.selected = row; this.messages = []; this.clearPendingDecisions(); this.error = '';
    this.draft = ''; this.sourceContext = ''; this.draftRevision = ''; this.draftLoading = true;
    this.localMessages = []; this.renderedMessages = []; this.echoedMessages.clear(); this.operationState = '';
    this.feedbackChanged();
    try {
      const saved = await new ChatDraftStore(this.local).read(this.target());
      if (!this.owns(generation, scope) || selection !== this.selectionGeneration) return;
      this.draft = saved.text; this.sourceContext = saved.context; this.draftRevision = saved.revision;
      await this.restoreOutbox(generation, scope);
    } finally { if (this.owns(generation, scope) && selection === this.selectionGeneration) this.draftLoading = false; }
    if (!this.owns(generation, scope) || selection !== this.selectionGeneration) return;
    this.chatVisible = this.isChat();
    this.feedbackChanged();
    if (this.chatVisible) await this.loadChat(row);
    // Dedicated resource workspaces own their reads and endpoint-specific loading/error state.
  }
  updateDraft(text: string): void {
    if (this.draftLoading) return;
    if (utf8Bytes(text) > 100000) { this.error = 'chat_attachment_text_too_large'; return; }
    if (text !== this.draft) this.draftRevision = util.generateRandomUUID();
    this.draft = text;
    if (this.draftTimer !== -1) clearTimeout(this.draftTimer);
    const generation = this.generation, scope = this.scope(), target = this.target();
    this.draftTimer = setTimeout(() => { this.draftTimer = -1; this.persistDraft().catch(() => { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }); }, 200);
  }
  async persistDraft(): Promise<void> {
    if (!this.profile.id || !this.selected.id) return;
    if (!this.draftRevision && (this.draft || this.sourceContext)) this.draftRevision = util.generateRandomUUID();
    const draft = new ChatDraft(); draft.text = this.draft; draft.context = this.sourceContext; draft.revision = this.draftRevision;
    await new ChatDraftStore(this.local).save(this.target(), draft);
  }
  async restoreLocalDelivery(): Promise<void> { await this.restoreOutbox(this.generation, this.scope()); }
  private async restoreOutbox(generation: number, scope: string): Promise<void> {
    const epoch = this.local.revision('outbox:' + scope), target = this.target(), repository = this.repository, entries = await new ChatDraftStore(this.local).list(scope);
    if (!this.owns(generation, scope) || target !== this.target() || !this.local.accepts('outbox:' + scope, epoch)) return;
    this.localMessages = entries;
    if (!this.sendAttempts.has(target)) {
      const now = Date.now(), last = entries.filter((entry: ChatOutboxEntry) => entry.target === target).pop();
      if (last && last.state === 'done' && last.acceptedAt > 0 && now < last.acceptedAt + ChatSendFeedback.acceptedWindow) {
        const attempt = this.makeSendFeedback(this.selected,last); attempt.accept(last.acceptedAt);
        this.sendAttempts.set(target,attempt); this.feedbackChanged();
      }
    }
    for (const entry of entries) if (this.local.accepts('outbox:' + scope, epoch) && entry.state === 'pending' && !entry.confirmedPending && !this.activeMessages.has(entry.id) && !this.unresolvedWrites.some((record: MutationRecord) => record.id === entry.id)) { entry.state = 'unknown'; await new ChatDraftStore(this.local).settle(scope, entry.id, 'unknown'); }
    for (const entry of entries) if (entry.target === target && entry.state === 'done') await this.completeLocalDelivery(entry, scope, repository);
    if (!this.owns(generation, scope) || target !== this.target() || !this.local.accepts('outbox:' + scope, epoch)) return;
    this.syncOutboxState(); this.reconcileMessageCopies();
  }
  private syncOutboxState(): void {
    const entries = this.localMessages.filter((entry: ChatOutboxEntry) => entry.target === this.target() && !this.echoedMessages.has(entry.id));
    const blocking = entries.find((entry: ChatOutboxEntry) => ['pending', 'unknown'].includes(entry.state));
    this.operationState = blocking?.state || (entries.length ? entries[entries.length - 1].state : '');
  }
  private sessionRow(row: ResourceRow = this.selected): ResourceRow {
    return this.sessions.find((item: ResourceRow) => item.machine === row.machine && (item.session || item.id) === (row.session || row.id)) || row;
  }
  imageLimits(row: ResourceRow = this.selected): ImageLimits {
    // A conversation carries its agent under `launcher`; with only `agent` read, an assistant conversation running
    // codex fell back to claude's limits (review P1).
    const data = this.sessionRow(row).data();
    return ImageLimits.forAgent(this.publishedImageLimits, Wire.text(data, 'agent') || Wire.text(data, 'launcher'));
  }
  private heldMessages(row: ResourceRow = this.selected): HeldAgentMessage[] {
    if (row.kind !== 'session') return [];
    const data = this.sessionRow(row).data();
    if (Wire.bool(data, 'assistant')) return [];
    const seen = new Set<string>(), held: HeldAgentMessage[] = [];
    for (const raw of Wire.rows(data, 'undelivered')) {
      const message = HeldAgentMessage.from(raw);
      if (message && !seen.has(message.id)) { seen.add(message.id); held.push(message); }
    }
    return held;
  }
  private publishMessages(): void {
    const held = this.heldMessages().filter((item: HeldAgentMessage) => !this.echoedMessages.has(item.id));
    const heldIds = new Set(held.map((item: HeldAgentMessage) => item.id));
    const heldBubbles = held.map((item: HeldAgentMessage): ChatMessage => {
      const display = messageDisplay(item.text), content = parseMessageContent(display.text);
      const message = new ChatMessage('held:' + item.id, 'user', content.text);
      message.held = item; message.at = Date.parse(item.at) || 0; message.localImageCount = item.images; message.source = display.sources;
      message.ownerTarget = this.target(); message.filePaths = content.files; message.imagePaths = content.images;
      return message;
    });
    const messages = this.localMessages.filter((entry: ChatOutboxEntry) => entry.target === this.target() && !this.echoedMessages.has(entry.id) && !heldIds.has(entry.id)).map((entry: ChatOutboxEntry): ChatMessage => {
      const display = messageDisplay(entry.text), content = parseMessageContent(display.text), message = new ChatMessage('local:' + entry.id, 'user', content.text);
      message.at = entry.at; message.source = display.sources; message.imagePaths = content.images; message.filePaths = content.files;
      message.ownerTarget = entry.target; message.localImageCount = entry.imageCount; message.localOperation = entry.id; message.deliveryState = entry.state; message.failed = entry.state === 'failed'; return message;
    });
    this.messages = this.renderedMessages.concat(heldBubbles, messages);
  }
  dictationLocaleForChat(): string {
    const language = this.language === 'default' ? i18n.System.getSystemLanguage() : this.language;
    return dictationLocale(dictationPreference.choice, dictationTexts(this.draft, this.revisionItems), dictationPreference.last, language);
  }
  rememberDictationLocale(locale: string): void { dictationPreference.remember(locale); }
  private publishTranscript(items: readonly TranscriptItem[], running: boolean): void {
    this.revisionItems = items.slice();
    const isMain = this.isMainActivityRow(this.selected);
    const projection = isMain ? splitBackgroundRounds(items, running) : undefined;
    this.activityOwner = isMain ? this.target() : '';
    this.activityRounds = projection?.rounds || [];
    this.renderedMessages = this.projectMessages(projection?.timeline || items, running);
    const feedback = this.sendAttempts.get(this.target());
    if (feedback) { feedback.observeItems(projection?.timeline || items); this.feedbackChanged(); }
    this.echoedMessages = new Set(items.filter((item: TranscriptItem) => item.role === 'user').map((item: TranscriptItem) => Wire.text(Wire.record(item as Object), 'client_msg_id')).filter((id: string) => !!id));
    this.reconcileMessageCopies();
  }
  private reconcileMessageCopies(): void {
    const heldIds = new Set(this.heldMessages().map((item: HeldAgentMessage) => item.id)), scope = this.scope(), generation = this.generation, target = this.target();
    const matched = this.localMessages.filter((entry: ChatOutboxEntry) => entry.target === target && (this.echoedMessages.has(entry.id) || heldIds.has(entry.id)));
    this.publishMessages();
    if (matched.length) {
      Promise.all(matched.map((entry: ChatOutboxEntry): Promise<void> => this.completeLocalDelivery(entry, scope))).then(async (): Promise<void> => {
        await new ChatDraftStore(this.local).discardEchoed(scope, matched.map((entry: ChatOutboxEntry) => entry.id));
        if (this.owns(generation, scope) && this.target() === target) { this.localMessages = this.localMessages.filter((entry: ChatOutboxEntry) => !matched.some((done: ChatOutboxEntry) => done.id === entry.id)); this.syncOutboxState(); this.publishMessages(); }
      }).catch(() => { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; });
    }
  }
  canCheckLocalMessage(id: string): boolean {
    const entry = this.localMessages.find((value: ChatOutboxEntry) => value.id === id && value.target === this.target());
    return this.connected && !!entry && entry.state === 'unknown' && !this.activeMessages.has(id) && !this.unresolvedWrites.some((record: MutationRecord) => record.id === id);
  }
  canRestoreMessage(id: string): boolean {
    const entry = this.localMessages.find((value: ChatOutboxEntry) => value.id === id && value.target === this.target());
    return !!entry && entry.state === 'failed' && (this.draft !== entry.draft || this.sourceContext !== entry.context);
  }
  async restoreMessageDraft(id: string): Promise<void> {
    const entry = this.localMessages.find((value: ChatOutboxEntry) => value.id === id && value.target === this.target());
    if (!entry || entry.state !== 'failed') return;
    const generation = this.generation, scope = this.scope(), target = this.target();
    this.cancelDraftTimer(); this.draft = entry.draft; this.sourceContext = entry.context; this.draftRevision = util.generateRandomUUID();
    try { await this.persistDraft(); } catch (_) { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }
  }
  private async completeLocalDelivery(entry: ChatOutboxEntry, scope: string, repository: HubRepository | undefined = this.repository): Promise<void> {
    const valid = (): boolean => !repository?.mutations?.scopeRetired && this.local.accepts('outbox:' + scope, entry.localEpoch);
    if (!valid()) return;
    if (entry.attachmentSnapshot) ChatAttachments.suppressStoredSnapshot(entry.attachmentSnapshot, entry.target);
    entry.state = 'done';
    if (this.target() === entry.target) {
      this.operationState = 'done';
      if (this.draftRevision === entry.revision) { this.cancelDraftTimer(); this.draft = ''; this.sourceContext = ''; this.draftRevision = ''; }
      this.publishMessages();
    }
    const store = new ChatDraftStore(this.local);
    const journal = repository?.mutations?.blocked().find((record: MutationRecord) => record.id === entry.id);
    if (journal) await repository?.mutations?.finish(journal, 'done');
    await store.settle(scope, entry.id, 'done', entry.acceptedAt); await store.acknowledge(entry.target, entry.revision);
    if (entry.attachmentSnapshot) {
      if (!this.context) throw new Error('chat_attachment_storage_unavailable');
      await ChatAttachments.commitStoredSnapshot(this.context, entry.attachmentSnapshot, entry.target, valid);
      if (!valid()) return;
      await store.attachmentsCommitted(scope, entry.id); entry.attachmentSnapshot = '';
    }
  }
  private async cachedChat(row: ResourceRow): Promise<void> {
    const generation = this.generation, scope = this.scope(), target = this.target(row);
    const saved = await this.local.get('transcript:' + target);
    if (!this.owns(generation, scope) || target !== this.target()) return;
    if (saved) { const reducer = new TranscriptReducer(); reducer.restore(JSON.parse(saved) as TranscriptSnapshot); this.publishTranscript(reducer.items, false); this.hasMoreMessages = false; }
    this.clearPendingDecisions(); this.error = 'offline';
  }
  async loadChat(row: ResourceRow): Promise<void> {
    if (this.demo && this.demoRepository) { this.publishTranscript(this.demoRepository.historyItems(row) as Object as TranscriptItem[], false); this.hasMoreMessages = false; this.transcriptHistoryReady = true; await this.refreshDecisions(); return; }
    const generation = this.generation, scope = this.scope(), target = this.target(row);
    if (!this.connected) { await this.cachedChat(row); return; }
    const sequence = ++this.decisionRefreshSequence;
    let interactions: Record<string, Object>[] = [], decisionsRead = false;
    try { interactions = await this.readInteractionSnapshot(); decisionsRead = true; }
    catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); }
    if (!this.owns(generation, scope) || this.target() !== target) return;
    if (decisionsRead) this.replacePendingDecisions(interactions, row, target, generation, scope, sequence);
    if (!this.transcript || this.transcriptKey !== target) {
      this.closeTranscript(); this.transcriptKey = target;
      this.transcript = new TranscriptModel(row, this.repo(), this.local, target, (items: readonly TranscriptItem[], hasMore: boolean, historyReady: boolean) => {
        if (!this.owns(generation, scope) || this.target() !== target) return;
        this.hasMoreMessages = hasMore; this.transcriptHistoryReady = historyReady; this.publishTranscript(items, this.turnWorking());
      }, (error: Error) => { if (this.owns(generation, scope) && this.target() === target) this.failure(error); });
      await this.transcript.open();
    } else await this.transcript.latest();
  }
  private projectMessages(items: readonly TranscriptItem[], running: boolean): ChatMessage[] {
    const turns: TranscriptItem[][] = []; let turn: TranscriptItem[] = [];
    const isRecap = (item: TranscriptItem): boolean => item.kind === 'recap' && item.role === 'system' && !!item.id && !!item.text?.trim();
    items.forEach((item: TranscriptItem) => {
      const startsTurn = item.wake === true || (item.role === 'user' && item.kind === 'text');
      if (turn.length && (startsTurn || isRecap(item) !== isRecap(turn[0]))) {
        turns.push(turn); turn = [];
      }
      turn.push(item);
    });
    if (turn.length) turns.push(turn);
    const messages: ChatMessage[] = [];
    turns.forEach((group: TranscriptItem[], index: number) => {
      if (isRecap(group[0])) {
        const message = new ChatMessage('recap:' + group[0].id, 'recap', group[group.length - 1].text || '');
        message.ownerTarget = this.target();
        message.itemId = group[0].id;
        message.at = this.itemTime(group[0]);
        message.terminalRecap = index === turns.length - 1;
        message.recaps = group.map((item: TranscriptItem): ConversationRecapItem => {
          return { id: item.id, at: this.itemTime(item), text: item.text || '' };
        });
        messages.push(message);
        return;
      }
      const projection = projectToolTurn(group[0].id, group, running && index === turns.length - 1);
      projection.userMessages.forEach((item: TranscriptItem) => { const display = messageDisplay(item.text || ''); const message = new ChatMessage(item.id, 'user', display.text); message.ownerTarget = this.target(); message.itemId = item.id; message.sourceFingerprint = typeof item['source_fingerprint'] === 'string' ? item['source_fingerprint'] as string : ''; message.source = display.sources; const content = parseMessageContent(display.text); message.text = content.text; message.imagePaths = content.images; message.filePaths = content.files; message.at = this.itemTime(item); messages.push(message); });
      projection.userImages.forEach((item: TranscriptItem) => { const message = new ChatMessage(item.id, 'user'); message.imageId = item.id; message.ownerTarget = this.target(); message.itemId = item.id; message.sourceFingerprint = typeof item['source_fingerprint'] === 'string' ? item['source_fingerprint'] as string : ''; message.at = this.itemTime(item); messages.push(message); });
      if (projection.summaryVisible) {
        const summary = new ChatMessage(projection.workKey, 'work');
        summary.at = this.itemTime(group[0]);
        summary.toolCount = projection.toolCount; summary.thoughtCount = projection.thoughtCount; summary.running = projection.running;
        summary.work = projection.inspectionSteps.map((step: WorkInspectionStep) => this.workText(step)).filter((value: string) => !!value).join('\n\n');
        summary.raw = JSON.stringify(group); messages.push(summary);
      }
      if (projection.visibleAssistantText) { const message = new ChatMessage('visible-' + group[0].id, 'assistant', projection.visibleAssistantText.text || ''); message.ownerTarget = this.target(); message.itemId = projection.visibleAssistantText.id; message.sourceFingerprint = typeof projection.visibleAssistantText['source_fingerprint'] === 'string' ? projection.visibleAssistantText['source_fingerprint'] as string : ''; message.at = this.itemTime(projection.visibleAssistantText); messages.push(message); }
    });
    return messages;
  }
  private itemTime(item: TranscriptItem): number {
    const value = item['ts']; const parsed = typeof value === 'string' ? Date.parse(value) : 0;
    return Number.isFinite(parsed) ? parsed : 0;
  }
  private workText(step: WorkInspectionStep): string {
    if (step.kind === 'narration') return step.item.text || '';
    if (step.kind === 'fold') return tr('work_summary') + ' · ' + (step.toolCount + step.thoughtCount);
    const use = Wire.record(JSON.parse(JSON.stringify(step.use)) as Object);
    const name = Wire.text(use, 'tool', Wire.text(use, 'name', tr('work_summary')));
    const input = Wire.object(use, 'input');
    return [name, Object.keys(input).length ? JSON.stringify(input, null, 2) : step.use.text || '',
      step.results.map((item: TranscriptItem) => item.text || '').join('\n')].filter((value: string) => !!value).join('\n');
  }
  private closeTranscript(): void { this.transcriptHistoryReady = false; this.transcript?.close(); this.transcript = undefined; this.transcriptKey = ''; this.revisionItems = []; }
  showConversation(row: ResourceRow): void { this.chatVisible = true; this.feedbackChanged(); this.loadChat(row).catch((error: Error) => this.failure(error)); }
  hideConversation(releaseTools: boolean = true): void { this.chatVisible = false; this.feedbackChanged(); this.closeTranscript(); this.clearPendingDecisions(); if (releaseTools) this.releaseTerminal(); this.persistDraft().catch(() => this.error = 'failed'); }
  decisionSourceMessage(toolUseId: string): string {
    return this.transcriptKey === this.target() ? this.transcript?.decisionSource(toolUseId) || '' : '';
  }
  async loadOlder(): Promise<boolean> { try { if (!this.transcript) return false; await this.transcript.older(); return true; } catch (error) { this.failure(error as Error); return false; } }
  async expandWork(message: ChatMessage): Promise<void> {
    try {
      const items: TranscriptItem[] = JSON.parse(message.raw) as TranscriptItem[];
      for (const item of items) if (item.kind === 'fold') await this.transcript?.unfold(item.id);
    } catch (error) { this.failure(error as Error); }
  }
  private decodeDecision(data: Record<string, Object>): Decision {
    const decision = decodeInteractionDecision(data);
    const nested = Wire.object(data, 'interaction');
    if (!decision.revision) decision.revision = Wire.text(data, 'revision', Wire.text(nested, 'revision'));
    decision.machine = Wire.text(data, 'machine_id', decision.machine);
    decision.session = Wire.text(data, 'session_id', decision.session);
    if (this.operationBlocked('/api/v1/interactions/' + segment(decision.id) + '/resolve')) decision.status = 'unknown';
    return decision;
  }
  sentForwards(message:ChatMessage):Record<string,Object>[] {
    if(message.ownerTarget!==this.target() || message.role!=='user' || !message.source)return [];
    const original=this.revisionItems.find((item:TranscriptItem)=>item.id===(message.itemId||message.id));
    return original?.text?forwardPrefix(original.text).entries.filter((r:Record<string,Object>)=>Wire.text(r,'kind')==='forward'):[];
  }
  sentOtherSourceTitle(message:ChatMessage):string {
    if (!this.sentForwards(message).length) return message.source;
    const item=this.revisionItems.find((v:TranscriptItem) => v.id===(message.itemId || message.id));
    return item?.text ? forwardPrefix(item.text).entries.filter((r:Record<string,Object>) => Wire.text(r,'kind')!=='forward')
      .map((r:Record<string,Object>) => Wire.text(r,'title')).filter((v:string)=>!!v).join(' · ') : message.source;
  }
  forwardedOrigin(ref:Record<string,Object>):ResourceRow|undefined {
    const link=forwardOrigin(ref);if(!link)return undefined;
    const dir=this.forwardDirectory();
    return dir.conversations.concat(dir.sessions).find(r=>(!r.group||r.group===this.profile.id) &&
      r.machine===link.machine && (r.session||r.id)===link.session && this.capabilities(r).canRead);
  }
  sentForwardGroups(message:ChatMessage):Record<string,Object>[][] {
    if(message.ownerTarget!==this.target() || message.role!=='user' || !message.source)return [];
    const original=this.revisionItems.find((item:TranscriptItem)=>item.id===(message.itemId||message.id));
    return original?.text?forwardGroups(forwardPrefix(original.text).entries):[];
  }
  forwardDraftGroups():Record<string,Object>[][] {return forwardGroups(this.draftContexts(this.sourceContext));}
  hasForwardContext():boolean { return this.sourceContext.includes('"forward"') && draftForwardRows(this.sourceContext).length>0; }
  forwardContexts():Record<string,Object>[] { return draftForwardRows(this.sourceContext); }
  hasSendableContext():boolean {
    if (!this.sourceContext) return false;
    try {
      return this.draftContexts(this.sourceContext).some((ref:Record<string,Object>) =>
        !!Wire.text(ref,'kind') && !!(Wire.text(ref,'text') || Wire.text(ref,'id') || Wire.text(ref,'msg') || Wire.text(ref,'item_id') || Wire.text(ref,'title')));
    } catch (_) { return false; }
  }
  prepareMessageText(text:string=this.draft,attachmentNoteKey:string=''):string {
    const hasContext=this.hasSendableContext();
    if (!text.trim() && attachmentNoteKey) text=tr(attachmentNoteKey);
    else if (!text.trim() && hasContext) {
      const forwards=this.forwardContexts().length;
      text=tr(forwards>1 ? 'chat_forward_several_note' : forwards===1 ? 'chat_forward_single_note' : 'chat_context_only_note');
    }
    return hasContext ? withSourceContext(text,this.sourceContext,this.features) : text;
  }
  /** Target lists use only the directory captured for the originating Hub. */
  forwardDirectory():ClientModel {
    const root=this.directoryParent;
    return root && root.profile.id===this.profile.id && root.profile.hubId===this.profile.hubId ? root : this;
  }
  canForwardTo(row:ResourceRow,ended:boolean=true):boolean {
    if (!this.connected || (row.group && row.group!==this.profile.id) || !['session','conversation'].includes(row.kind) ||
      !row.id || !row.machine || !this.profile.id) return false;
    const endpoints=this.forwardDirectory();
    const current=endpoints.conversations.concat(endpoints.sessions).find(r=>r.kind===row.kind && r.id===row.id &&
      r.machine===row.machine && (!r.group || r.group===this.profile.id) && (!r.manager || !row.manager || r.manager===row.manager));
    if(!current)return false;
    const d=current.data(), effective=this.capabilities(current), caps=Wire.object(d,'capabilities');
    if(Wire.bool(d,'deleted') || Wire.has(d,'capabilities') && !Wire.bool(caps,'send'))return false;
    if (!effective.canSend || Wire.bool(d,'archived') || Wire.bool(d,'health') || Wire.bool(d,'is_health') ||
        Wire.text(d,'space')==='health' || Wire.text(d,'purpose')==='health' || Wire.text(d,'kind')==='health') return false;
    if (this.selected.machine===row.machine && (this.selected.session||this.selected.id)===(row.session||row.id)) return false;
    if (row.kind==='conversation' || Wire.bool(d,'assistant')) return true;
    const machine=endpoints.machines.find((m:ResourceRow)=>m.id===row.machine && (!m.group || m.group===this.profile.id));
    return !!machine && Wire.bool(machine.data(),'online') && (ended || current.state.toLowerCase()!=='offline');
  }
  async receiveForward(target:ResourceRow,refs:Record<string,Object>[],images:DraftForwardImage[],transfer:string,admit:()=>boolean):Promise<void> {
    const generation=this.connectionGeneration(),scope=this.scope();
    const key=JSON.stringify([scopeKey({groupId:this.profile.id,managerId:target.manager || this.profile.manager}),target.kind,target.machine,target.id]);
    const current=():boolean=>admit() && this.owns(generation,scope) && this.canForwardTo(target);
    if(!current()) throw new Error('request_updated');
    const root=this.directoryParent || this, viewers:ClientModel[]=[root,...Array.from(root.views.values())];
    const active=viewers.filter((v:ClientModel)=>v.selected.id && v.target()===key && !v.draftLoading && !v.retired);
    for(const v of active) if(v.draftTimer!==-1){v.cancelDraftTimer();await v.persistDraft();}
    const snapshots=new Map<ClientModel,string>();
    for(const v of active)snapshots.set(v,v.draftRevision);
    const store=new ChatDraftStore(this.local);
    let merged=await store.appendForwards(key,refs,images,transfer,current);
    if(!current())throw new Error('request_updated');
    for(const v of active) if(v.target()===key && !v.retired) {
      if(v.draftRevision!==snapshots.get(v)){
        v.cancelDraftTimer();await v.persistDraft();
        merged=await store.appendForwards(key,refs,images,transfer,current);
      }
      if(!current() || v.target()!==key || v.retired)continue;
      v.draft=merged.text;v.sourceContext=merged.context;v.draftRevision=merged.revision;
    }
  }
  async removeForward(item:string,url:string):Promise<void> {
    const before=this.sourceContext,key=this.target(),old=this.draftRevision;
    const refs=this.draftContexts(before);const index=refs.findIndex((v:Record<string,Object>)=>Wire.text(v,'kind')==='forward' && Wire.text(v,'msg')===item && Wire.text(v,'url')===url);
    if(index<0)return;refs.splice(index,1);this.sourceContext=refs.length?JSON.stringify(Wire.payload(['contexts'],[refs])):'';this.draftRevision=util.generateRandomUUID();
    const current=this.draftRevision;
    try { await this.persistDraft();await new ChatDraftStore(this.local).discardForwardMedia(key,before,this.sourceContext); }
    catch (_) { if(this.target()===key && this.draftRevision===current){this.sourceContext=before;this.draftRevision=old;this.error='cache_unavailable';} }
  }
  async send(admit?: () => boolean): Promise<boolean> {
    if (admit && !admit()) return false;
    const text = this.draft;
    if (utf8Bytes(text) > 100000) { this.error = 'chat_attachment_text_too_large'; return false; }
    if (this.draftLoading || !this.canSend() || (!text.trim() && !this.hasSendableContext()) || ['pending', 'unknown'].includes(this.operationState)) return false;
    if (this.demo && this.demoRepository) {
      const prepared = this.prepareMessageText(text); if (utf8Bytes(prepared) > 100000) { this.error = 'chat_attachment_request_too_large'; return false; }
      try { this.demoRepository.sendLocal(this.selected, prepared); this.sourceContext = ''; this.updateDraft(''); await this.loadChat(this.selected); return true; }
      catch (error) { this.failure(error as Error); return false; }
    }
    const preparedText = this.prepareMessageText(text); if (utf8Bytes(preparedText) > 100000) { this.error = 'chat_attachment_request_too_large'; return false; }
    return this.dispatchMessage(this.repo(), this.selected, preparedText, [], this.generation, this.scope(), this.target(), text, this.sourceContext, '', admit);
  }
  async sendPrepared(prepared: PreparedAttachmentSend, draft: string, context: string, admit?: () => boolean): Promise<boolean> {
    if (admit && !admit() || !prepared.repository || this.draftLoading || !this.canSend() || ['pending', 'unknown'].includes(this.operationState) ||
      !this.owns(prepared.generation, prepared.scope) || this.target() !== prepared.modelTarget || this.repository !== prepared.repository ||
      this.target(prepared.row) !== prepared.modelTarget || (!prepared.text.trim() && !prepared.images.length)) return false;
    if (this.draft !== draft || this.sourceContext !== context) { this.error = 'chat_attachment_selection_changed'; return false; }
    return this.dispatchMessage(prepared.repository, prepared.row, prepared.text, prepared.images, prepared.generation, prepared.scope, prepared.modelTarget, draft, context, JSON.stringify(prepared.snapshot), admit);
  }
  private async dispatchMessage(repo: HubRepository, row: ResourceRow, text: string, images: Object[], generation: number, scope: string, target: string, draft: string, context: string, attachmentSnapshot: string = '', admit?: () => boolean): Promise<boolean> {
    const store = new ChatDraftStore(this.local), entry = new ChatOutboxEntry();
    const hub = this.profile.id, pairing = this.pairingRevision();
    const paired = (): boolean => (ClientModel.pairingEpochs.get(hub) || 0) === pairing && this.local.accepts('outbox:' + scope, entry.localEpoch);
    if (!this.draftRevision) this.draftRevision = util.generateRandomUUID();
    entry.id = util.generateRandomUUID(); entry.target = target; entry.revision = this.draftRevision; entry.text = text; entry.context = context; entry.draft = draft; entry.imageCount = images.length; entry.attachmentSnapshot = attachmentSnapshot; entry.at = Date.now(); entry.localEpoch = this.local.revision('outbox:' + scope);
    entry.afterSequence = this.revisionItems.length ? this.revisionItems[this.revisionItems.length-1].seq : 0;
    const feedback = this.makeSendFeedback(row,entry);
    this.sendAttempts.set(target,feedback); this.feedbackChanged();
    this.activeMessages.add(entry.id); this.cancelDraftTimer(); this.operationState = 'pending'; this.error = ''; let dispatched = false, staged = false;
    try {
      const captured = new ChatDraft(); captured.text = draft; captured.context = context; captured.revision = entry.revision;
      await store.save(target, captured);
      if (admit && !admit() || !this.owns(generation, scope) || this.target() !== target || this.repository !== repo) { feedback.finish(); this.feedbackChanged(); this.activeMessages.delete(entry.id); if (this.owns(generation, scope) && this.target() === target) { this.operationState = 'failed'; this.error = 'request_updated'; } return false; }
      if (context.includes('"forward"') && draftForwardRows(context).length) {
        const current=():boolean=>this.owns(generation,scope) && this.target()===target && this.repository===repo && this.draftRevision===entry.revision && (!admit || admit());
        const extras=await store.forwardedImages(target,context,4-images.length,current);
        images=images.concat(extras);entry.imageCount=images.length;
        if (utf8Bytes(JSON.stringify(Wire.payload(['text','images'],[text,images])))>=4*1024*1024) throw new Error('chat_attachment_request_too_large');
      }
      await store.stage(scope, entry); staged = true;
      if (admit && !admit() || !this.owns(generation, scope) || this.target() !== target || this.repository !== repo || this.draftRevision !== entry.revision || !this.canSend()) { feedback.finish(); this.feedbackChanged(); await store.settle(scope, entry.id, 'failed'); this.activeMessages.delete(entry.id); entry.state = 'failed'; if (this.owns(generation, scope) && this.target() === target) { this.operationState = 'failed'; this.error = 'request_updated'; this.publishMessages(); } return false; }
      this.localMessages = this.localMessages.concat([entry]); this.publishMessages();
      dispatched = true;
      const reply = await repo.send(row, text, entry.id, '', images, (): boolean => (!admit || admit()) && this.owns(generation, scope) && this.target() === target && this.repository === repo && this.draftRevision === entry.revision && !this.decision && this.canSend(), this.imageLimits(row).requestBytes);
      if (reply && Wire.text(reply,'delivered') === 'later') feedback.finish();
      else if (paired()) {
        const now = Date.now(); feedback.accept(now);
        if (feedback.visible(now) === 'processing') entry.acceptedAt = now;
      }
      this.feedbackChanged();
      if (reply && row.kind === 'session' && Wire.text(reply, 'delivered') === 'later') {
        const raw = Wire.object(reply, 'undelivered'), held = HeldAgentMessage.from(raw);
        if (held?.id === entry.id) {
          if (!paired()) { this.activeMessages.delete(entry.id); return true; }
          this.heldOwner().heldChanged(scope);
          if (entry.state !== 'done' && this.owns(generation, scope) && this.repository === repo) {
            const source = this.sessionRow(row), data = source.data(), list = Wire.rows(data, 'undelivered');
            if (!list.some((item: Record<string, Object>) => Wire.text(item, 'id') === entry.id)) {
              data['undelivered'] = list.concat([raw]); source.raw = JSON.stringify(data);
            }
            // The other device may already have canceled this operation while its initial reply was in flight.
            this.queueRefresh();
          }
          this.activeMessages.delete(entry.id);
          // The manager now owns this operation. A later cancellation must not resurrect a local "sent" bubble.
          let cached = true;
          try { await store.cacheHeldMessage(scope, target, row, entry.id, raw, paired); }
          catch (_) { cached = false; if (this.owns(generation, scope)) this.error = 'cache_unavailable'; }
          try {
            await this.completeLocalDelivery(entry, scope, repo);
            if (cached) await store.discardEchoed(scope, [entry.id]);
            if (this.owns(generation, scope) && this.target() === target) {
              this.localMessages = this.localMessages.filter((item: ChatOutboxEntry) => item.id !== entry.id);
              this.syncOutboxState(); this.publishMessages();
            }
          } catch (_) { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }
          return true;
        }
      }
    } catch (error) {
      feedback.finish(); this.feedbackChanged();
      if (entry.state === 'done') {
        this.activeMessages.delete(entry.id);
        try { await this.completeLocalDelivery(entry, scope, repo); } catch (_) { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }
        return true;
      }
      const pending = error instanceof HttpFailure && error.resultPending, unknown = error instanceof HttpFailure ? error.resultUnknown : dispatched;
      this.activeMessages.delete(entry.id); entry.state = pending ? 'pending' : unknown ? 'unknown' : 'failed'; entry.confirmedPending = pending;
      if (staged) { try { await store.settle(scope, entry.id, entry.state); } catch (_) {} }
      if (this.owns(generation, scope) && this.target() === target) {
        this.operationState = entry.state; this.failure(error as Error); if (unknown) this.error = pending ? 'pending' : 'result_unknown'; this.publishMessages();
      }
      return false;
    }
    this.activeMessages.delete(entry.id);
    // The acknowledgement belongs to the captured draft even if its destination is no longer on screen.
    try { await this.completeLocalDelivery(entry, scope, repo); } catch (_) { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }
    if (this.owns(generation, scope) && this.target() === target) {
      try { await this.loadChat(row); } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); }
    }
    return true;
  }
  async useMessageContext(message: ChatMessage): Promise<void> {
    if (!message.text || message.ownerTarget !== this.target() || !this.messages.some((item: ChatMessage) => item.id === message.id && item.text === message.text)) return;
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (utf8Bytes(message.text) > 100000) { this.error = 'chat_attachment_text_too_large'; return; }
    this.sourceContext = JSON.stringify(Wire.payload(['kind', 'title', 'item_id', 'machine_id', 'session_id', 'manager', 'text'],
      ['message', this.selected.title || tr('message'), message.itemId || message.id, this.selected.machine, this.selected.session || this.selected.id, this.profile.manager, message.text]));
    this.draftRevision = util.generateRandomUUID();
    try { await this.persistDraft(); } catch (_) { if (this.owns(generation, scope) && this.target() === target) this.error = 'cache_unavailable'; }
  }
  async quoteMessageContext(message: ChatMessage, author: string, selection?: string): Promise<boolean> {
    const target=this.target(),generation=this.generation,scope=this.scope();
    if (this.draftLoading || !message.text || message.ownerTarget!==target ||
      !this.messages.some((item:ChatMessage) => item.id===message.id && item.text===message.text)) return false;
    const text=selection===undefined ? message.text : selection.trim();
    if (!text || selection!==undefined && !liftedMessageWords(message.text,message.role==='user').includes(text)) return false;
    const previous=this.sourceContext,previousRevision=this.draftRevision,refs=this.draftContexts(previous);
    const id=message.itemId || message.id,part=selection===undefined ? '' : 'selection';
    if (id && refs.some((ref:Record<string,Object>) => Wire.text(ref,'kind')==='quote' && Wire.text(ref,'msg')===id &&
      Wire.text(ref,'part')===part && (!part || Wire.text(ref,'text')===text))) return true;
    const ref=Wire.payload(['kind','title','url','msg','text'],['quote',author,'',id,text]);
    if (part) ref['part']=part;
    refs.push(ref);
    const next=this.serializeDraftContexts(refs);
    if (utf8Bytes(next)>100000) { this.error='chat_attachment_text_too_large'; return false; }
    const revision=util.generateRandomUUID(); this.sourceContext=next; this.draftRevision=revision;
    const current=():boolean => this.owns(generation,scope) && this.target()===target && this.draftRevision===revision;
    try { await this.persistDraft(); return current(); }
    catch (_) {
      if (current()) { this.sourceContext=previous; this.draftRevision=previousRevision; this.error='cache_unavailable'; }
      return false;
    }
  }
  @Trace interruptPendingKey: string = '';
  private interruptIdentity(): string { return JSON.stringify([this.generation, this.target()]); }
  private turnWorking(): boolean {
    const row = this.sessionRow();
    const data = row.data(), status = Wire.text(data, 'status', row.state);
    return row.state === 'working' || status === 'working' || Wire.bool(data, 'working') ||
      this.selected.kind === 'session' && status === 'blocked';
  }
  assistantReplyWorking(): boolean { return this.selected.kind === 'conversation' && this.turnWorking(); }
  agentQueuesMessages(): boolean { return this.selected.kind === 'session' && this.turnWorking(); }
  stopPending(): boolean { return this.interruptPendingKey === this.interruptIdentity(); }
  canStop(): boolean {
    const path = '/api/v1/sessions/' + encodeURIComponent(this.selected.machine) + '/' + encodeURIComponent(this.selected.session || this.selected.id) + '/interrupt';
    return this.canInterrupt() && this.turnWorking() && this.interruptPendingKey !== this.interruptIdentity() && !this.operationBlocked(path);
  }
  async stop(admission?: () => boolean): Promise<boolean> {
    if (!this.canStop() || admission && !admission()) return false;
    const generation = this.generation, scope = this.scope(), target = this.target(), row = this.selected, repo = this.repo();
    const feedback = this.sendAttempts.get(target);
    const pending = this.interruptIdentity(), admit = (): boolean => this.owns(generation, scope) && this.target() === target && this.repository === repo && this.canInterrupt() && this.turnWorking() && (!admission || admission());
    this.interruptPendingKey = pending;
    try { await repo.stop(row, admit); } catch (error) {
      if (this.owns(generation, scope) && this.target() === target && this.repository === repo) this.failure(error as Error);
      return false;
    } finally { if (this.interruptPendingKey === pending) this.interruptPendingKey = ''; }
    if (feedback && this.owns(generation,scope) && this.sendAttempts.get(target) === feedback) await this.endSendFeedback(scope,target,feedback);
    if (!this.owns(generation, scope) || this.target() !== target || this.repository !== repo) return false;
    if (this.demo && this.demoRepository === repo) this.refreshDemo();
    return true;
  }
  async answer(option: string, values: Record<string, Object>, expected?: Decision, admit?: () => boolean): Promise<void> {
    if (admit && !admit()) return;
    if (!this.canApprove()) return;
    const row = this.selected, target = this.target();
    let decision = expected ? this.pendingDecisions.find((item: Decision): boolean => item.id === expected.id && item.raw === expected.raw) : this.decision;
    if (!decision && expected && !this.pendingDecisionTarget && this.pendingDecisions.length === 0 && this.decision === expected) decision = expected;
    if (!decision || expected && (expected.id !== decision.id || expected.raw !== decision.raw || expected.machine !== decision.machine || expected.session !== decision.session)) { this.error = 'request_updated'; return; }
    const session = row.session || row.id;
    if (this.pendingDecisionTarget && (this.pendingDecisionTarget !== target || !this.pendingDecisions.some((item: Decision): boolean => item === decision) ||
      decision.machine !== row.machine || decision.session !== session || row.manager && row.manager !== this.profile.manager)) {
      this.error = 'request_updated'; return;
    }
    if (!interactionCanSubmit(decision)) return;
    const personaQuestion = isPersonaAskUserQuestion(decision, row.kind);
    const request = new Decision();
    request.id = decision.id; request.raw = decision.raw; request.kind = decision.kind; request.revision = decision.revision;
    request.machine = decision.machine; request.session = decision.session; request.prompt = decision.prompt;
    request.options = decision.options.slice(); request.fields = decision.fields.slice(); request.status = decision.status;
    const generation = this.generation, scope = this.scope(), repo = this.repo();
    const current = (): boolean => this.owns(generation, scope) && this.target() === target && this.repository === repo &&
      (this.pendingDecisionTarget === target ? this.pendingDecisions.some((item: Decision): boolean => item === decision) :
        !this.pendingDecisionTarget && this.pendingDecisions.length === 0 && this.decision === decision);
    const ownedDecision = decision, previousState = this.operationState; let retiredBeforeSend = false;
    const dispatch = (): boolean => { const allowed = current() && ownedDecision.status === 'pending' && ownedDecision.raw === request.raw && this.canApprove() && (!admit || admit()); if (!allowed) retiredBeforeSend = true; return allowed; };
    decision.status = 'pending';
    if (!personaQuestion) this.operationState = 'pending';
    try {
      await repo.resolve(request, option, values, util.generateRandomUUID(), dispatch);
    } catch (error) {
      if (!current()) return;
      if (retiredBeforeSend) {
        if (decision.status === 'pending' && decision.raw === request.raw) {
          decision.status = 'open';
          if (!personaQuestion && this.operationState === 'pending') this.operationState = previousState;
        }
        return;
      }
      decision.status = error instanceof HttpFailure && error.resultPending ? 'pending' : error instanceof HttpFailure && error.resultUnknown ? 'unknown' : 'open';
      if (!personaQuestion) this.operationState = ['pending', 'unknown'].includes(decision.status) ? decision.status : 'failed';
      this.failure(error as Error);
      if (error instanceof HttpFailure && (error.status === 409 || error.status === 410)) await this.loadChat(row);
      return;
    }
    if (!current()) return;
    decision.status = 'resolved';
    if (!personaQuestion) this.operationState = 'done';
    if (this.demo && this.demoRepository === repo) this.refreshDemo();
    // A refresh failure cannot turn an acknowledged approval into another sendable decision.
    try { await this.loadChat(row); } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); }
  }
  async listResources(kind: string): Promise<void> {
    this.error = ''; this.busy = true; const generation = this.generation, scope = this.scope();
    try {
      const rows = kind === 'todo' ? await this.repo().todos() : kind === 'knowledge' ? await this.repo().knowledge(this.knowledgeKind) : await this.repo().inbox();
      if (!this.owns(generation, scope)) return;
      this.resourceRows = rows;
      if (kind === 'todo') this.todos = rows; else if (kind === 'knowledge') this.knowledge = rows; else this.inbox = rows;
    } catch (error) { if (this.owns(generation, scope)) this.failure(error as Error); }
    finally { if (this.owns(generation, scope)) this.busy = false; }
  }
  async saveResource(row: ResourceRow, title: string, content: string, done?: boolean): Promise<boolean> {
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (row.manager !== this.profile.manager) return false;
    try {
      const repo = this.repo(); await repo.save(row, title, content, done);
      if (!this.owns(generation, scope)) return false;
      await this.listResources(row.kind);
      if (!this.owns(generation, scope) || this.target() !== target) return false;
      const refreshed = this.resourceRows.find((item: ResourceRow) => item.key() === row.key());
      if (refreshed && this.selected.key() === row.key()) this.selected = refreshed;
      return true;
    } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); return false; }
  }
  async deleteResource(row: ResourceRow): Promise<boolean> {
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (row.manager !== this.profile.manager) return false;
    try {
      const repo = this.repo(); await repo.remove(row);
      if (!this.owns(generation, scope)) return false;
      await this.listResources(row.kind); return this.owns(generation, scope) && this.target() === target;
    } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); return false; }
  }
  private draftContexts(context: string): Record<string, Object>[] {
    const saved = Wire.parse(context), contexts = Wire.rows(saved, 'contexts').slice();
    if (!contexts.length && Wire.text(saved, 'kind')) contexts.push(saved);
    return contexts;
  }
  async addDraftContext(row: ResourceRow, expectedTarget: string, generation: number, scope: string): Promise<boolean> {
    const current = (): boolean => this.owns(generation, scope) && this.target() === expectedTarget && this.canCompose() &&
      (!row.group || row.group === this.profile.id) && row.manager === this.profile.manager;
    if (!current() || this.draftLoading || !['knowledge', 'todo', 'inbox'].includes(row.kind)) return false;
    const previous = this.sourceContext, previousRevision = this.draftRevision;
    const contexts = this.draftContexts(previous);
    if (contexts.some((item: Record<string, Object>) => Wire.text(item, 'id') === row.id && Wire.text(item, 'kind') === row.kind && Wire.text(item, 'manager') === row.manager && (row.kind !== 'knowledge' || Wire.text(item, 'entry_kind') === row.path))) return true;
    const reference = Wire.payload(['kind', 'id', 'title', 'manager'], [row.kind, row.id, row.title || row.id, row.manager]);
    if (row.kind === 'knowledge') reference['entry_kind'] = row.path;
    contexts.push(reference);
    const next = JSON.stringify(Wire.payload(['contexts'], [contexts]));
    if (next.length > 65536 || contexts.length > 16 || !current()) return false;
    const revision = util.generateRandomUUID(); this.sourceContext = next; this.draftRevision = revision;
    try { await this.persistDraft(); return current() && this.draftRevision === revision; }
    catch (_) {
      if (current() && this.draftRevision === revision) { this.sourceContext = previous; this.draftRevision = previousRevision; this.error = 'cache_unavailable'; }
      return false;
    }
  }

  async handoff(row: ResourceRow): Promise<ResourceRow> {
    const owner = row.manager, generation = this.generation, scope = this.scope();
    const view = await this.viewFor(row);
    if (!this.owns(generation, scope)) throw new Error('request_updated');
    if (view !== this) { const target = await view.handoff(row); if (!this.owns(generation, scope)) throw new Error('request_updated'); return target; }
    const target = await this.openMain();
    if (!this.owns(generation, scope) || this.selected.key() !== target.key()) throw new Error('request_updated');
    const contexts = this.draftContexts(this.sourceContext);
    const ref = Wire.payload(['kind', 'id', 'title', 'manager'], [row.kind, row.id, row.title, owner]);
    if (!contexts.some((item: Record<string, Object>) => Wire.text(item, 'id') === row.id && Wire.text(item, 'kind') === row.kind)) contexts.push(ref);
    this.sourceContext = JSON.stringify(Wire.payload(['contexts'], [contexts])); this.draftRevision = util.generateRandomUUID();
    await this.persistDraft();
    if (!this.owns(generation, scope) || this.selected.key() !== target.key()) throw new Error('request_updated'); return target;
  }
  async pair(pairing: Pairing, target: PairingTarget, admit?: () => boolean): Promise<void> {
    if (!this.vault) throw new Error('storage_unavailable');
    const epoch = this.forgetEpochs.get(target.hubId) || 0, generation = this.generation, scope = this.scope();
    const current = (): boolean => this.owns(generation, scope) && this.foreground && epoch === (this.forgetEpochs.get(target.hubId) || 0) && !this.forgettingProfiles.has(target.hubId) && (!admit || admit());
    if (!current()) throw new Error('request_updated');
    const route = pairing.sshRoute;
    try {
      const config = await pairing.redeem(target, tr('phone'), current);
      if (!current()) throw new Error('request_updated');
      await this.useEndpoint(config, target.name, current, route);
    } finally { route?.close(); }
  }
  private capabilities(row: ResourceRow = this.sessionRow()): EffectiveSessionCapabilities {
    const data = row.data(), caps = Wire.object(data, 'capabilities');
    const input: SessionCapabilityInput = { sourceKind: Wire.text(data, 'source_kind'),
      readOnly: Wire.bool(data, 'read_only', Wire.bool(data, 'readonly')), readOnlyReason: Wire.text(data, 'read_only_reason'),
      capabilities: { read: Wire.bool(caps, 'read'), send: Wire.bool(caps, 'send'), interrupt: Wire.bool(caps, 'interrupt'),
        approve: Wire.bool(caps, 'approve'), terminal: Wire.bool(caps, 'terminal'), attach: Wire.bool(caps, 'attach') } };
    return sessionCapabilities(input);
  }
  canAttach(): boolean { return this.connected && this.capabilities().canAttach; }
  async attach(): Promise<void> {
    if (!this.canAttach()) return; const generation = this.generation, scope = this.scope(), row = this.selected;
    try { await this.repo().call('/api/v1/sessions/' + segment(row.machine) + '/' + segment(row.session || row.id) + '/attach', 'POST');
      await this.refresh(); if (this.owns(generation, scope) && this.target(row) === this.target()) { const updated = this.sessions.find((item: ResourceRow) => item.key() === row.key()); if (updated) await this.select(updated); }
    } catch (error) { if (this.owns(generation, scope)) this.failure(error as Error); }
  }
  private decisionsReady(): boolean {
    if (!this.isChat() || this.decisionReadyTarget === this.target()) return true;
    // Preserve compatibility with callers that still provide only the blocking decision.
    return !this.pendingDecisionTarget && this.pendingDecisions.length === 0 && this.decision !== undefined;
  }
  private coldHeldPlaceholder(row: ResourceRow, data: Record<string, Object>): boolean {
    // A freshly restarted manager knows only this held operation's machine and session, until the runtime is listed.
    return this.features.includes('agent_message_hold') && Wire.text(data, 'status', row.state) === 'offline' &&
      !Wire.text(data, 'source_kind') && !Wire.text(data, 'agent') && !Wire.text(data, 'agent_label') &&
      !row.title && !row.path && !Wire.has(data, 'capabilities') && Wire.has(data, 'undelivered');
  }
  agentInputNotice(): string {
    if (this.selected.kind !== 'session') return '';
    const row = this.sessionRow(), data = row.data(), caps = Wire.object(data, 'capabilities');
    if (Wire.bool(data, 'assistant')) return '';
    const source = Wire.text(data, 'source_kind'), status = Wire.text(data, 'status', row.state);
    if ((!source || source === 'codex_history') && ['offline', 'interrupted'].includes(status) &&
      Wire.bool(caps, 'read') && Wire.bool(caps, 'terminal') && !Wire.bool(caps, 'send')) return 'agent_stopped';
    if (Wire.bool(data, 'deleted') || this.coldHeldPlaceholder(row, data) || !this.capabilities().canSend ||
      (Wire.has(data, 'capabilities') && !Wire.bool(caps, 'send'))) return 'read_only';
    const machine = this.machines.find((item: ResourceRow) => item.id === row.machine);
    if (this.connected && machine && !Wire.bool(machine.data(), 'online', true)) {
      return this.features.includes('agent_message_hold') ? 'agent_computer_holds' : 'agent_computer_offline';
    }
    return '';
  }
  // A follow-up while an Agent waits is a message to that session. It never resolves an interaction.
  // A legacy caller with only a blocking decision has no verified snapshot for this route.
  decisionBlocksMessage(): boolean {
    if (!this.decision) return false;
    return this.selected.kind !== 'session' || this.pendingDecisionTarget !== this.target() ||
      this.decisionReadyTarget !== this.pendingDecisionTarget || !this.pendingDecisions.includes(this.decision);
  }
  canSend(): boolean { return this.connected && this.canCompose() && this.decisionsReady() && !this.decisionBlocksMessage(); }
  canCompose(): boolean {
    const notice = this.agentInputNotice();
    return this.isChat() && this.capabilities().canSend && (!notice || notice === 'agent_computer_holds');
  }
  offersHeldAction(message: ChatMessage, retry: boolean): boolean {
    const expected = message.held;
    if (!expected || message.ownerTarget !== this.target() || !this.connected || this.demo || !this.profile.id ||
      !this.features.includes('agent_message_hold') || this.echoedMessages.has(expected.id)) return false;
    const row = this.sessionRow(), data = row.data(), capabilities = this.capabilities();
    if (!row.machine || !(row.session || row.id) || (row.group && row.group !== this.profile.id) ||
      (row.manager && row.manager !== this.profile.manager)) return false;
    // The manager keeps this read-capable placeholder while a runtime is absent from its computer.
    const archivedRuntime = Wire.text(data, 'source_kind') === 'runtime' && Wire.text(data, 'status', row.state) === 'offline';
    const heldPlaceholder = (archivedRuntime || this.coldHeldPlaceholder(row, data)) && capabilities.canRead &&
      !capabilities.readOnly && !Wire.bool(data, 'deleted');
    if (!this.canCompose() && !heldPlaceholder) return false;
    const held = this.matchingHeldInstruction(expected, row);
    return !!held && held.text === expected.text && held.images === expected.images && held.computer === expected.computer &&
      held.at === expected.at && (!retry || (held.state === 'failed' && !held.deleteOnly));
  }
  // An accepted Retry follows the manager's instruction even if the displayed message changes.
  private matchingHeldInstruction(expected: HeldAgentMessage, row: ResourceRow): HeldAgentMessage | undefined {
    return this.heldMessages(row).find((item: HeldAgentMessage) => item.id === expected.id && item.state === expected.state &&
      item.line === expected.line && item.deleteOnly === expected.deleteOnly);
  }
  private removeHeldMessage(row: ResourceRow, id: string): boolean {
    const data = row.data(), previous = Wire.rows(data, 'undelivered');
    const kept = previous.filter((value: Record<string, Object>) => Wire.text(value, 'id') !== id);
    if (kept.length === previous.length) return false;
    data['undelivered'] = kept; row.raw = JSON.stringify(data); return true;
  }
  private cachedHeldAction(receipt: HeldActionReceipt): Record<string, Object> {
    const expected = receipt.expected;
    if (receipt.deleted) return Wire.payload(['target', 'id', 'deleted'], [receipt.target, expected.id, true]);
    return Wire.payload(['target', 'id', 'state', 'line', 'deleteOnly'], [receipt.target, expected.id, expected.state, expected.line, expected.deleteOnly]);
  }
  canActOnHeld(message: ChatMessage, retry: boolean): boolean {
    if (!this.offersHeldAction(message, retry) || !message.held) return false;
    const path = this.heldActionPath(message.held.id);
    return !(retry && this.heldRetryPending(message)) && !this.heldActions.includes(JSON.stringify([message.ownerTarget, message.id])) &&
      !this.operationBlocked(path) && !this.operationBlocked(path + '/retry');
  }
  heldRetryPending(message: ChatMessage): boolean {
    return this.heldOwner().heldReceipts.some((receipt: HeldActionReceipt) => !receipt.deleted && receipt.target === message.ownerTarget &&
      receipt.pairing === this.pairingRevision() && receipt.expected.id === message.held?.id);
  }
  private heldActionPath(id: string): string {
    const row = this.selected;
    return '/api/v1/sessions/' + segment(row.machine) + '/' + segment(row.session || row.id) + '/held/' + segment(id);
  }
  async actOnHeld(message: ChatMessage, retry: boolean, admit?: () => boolean): Promise<void> {
    if (admit && !admit() || !this.canActOnHeld(message, retry) || !message.held) return;
    const row = this.selected, expected = message.held, id = expected.id, path = this.heldActionPath(id);
    const repo = this.repo(), scope = this.scope(), generation = this.generation, hub = this.profile.id, pairing = this.pairingRevision();
    const key = JSON.stringify([message.ownerTarget, message.id]);
    const current = (): boolean => (!admit || admit()) && this.owns(generation, scope) && this.pairingRevision() === pairing &&
      this.repository === repo && this.offersHeldAction(message, retry);
    this.heldActions = this.heldActions.concat([key]);
    try {
      await repo.call(path + (retry ? '/retry' : ''), retry ? 'POST' : 'DELETE', {}, '', current, false,
        (result: Record<string, Object>): boolean => Wire.bool(result, 'ok'));
      const paired = (): boolean => (ClientModel.pairingEpochs.get(hub) || 0) === pairing;
      const inScope = this.scope() === scope && !this.retired;
      if (paired() && (!retry || !inScope || this.matchingHeldInstruction(expected, row))) {
        const owner = this.heldOwner(); owner.heldChanged(scope);
        const receipt: HeldActionReceipt = { scope: scope, pairing: pairing, target: message.ownerTarget, expected: expected, deleted: !retry };
        owner.heldReceipts = owner.heldReceipts.filter((old: HeldActionReceipt) => old.target !== receipt.target || old.expected.id !== id).concat([receipt]);
        if (inScope && !retry) { this.removeHeldMessage(this.sessionRow(row), id); this.publishMessages(); }
        try { await new ChatDraftStore(this.local).cacheHeldMessage(scope, message.ownerTarget, row, id, undefined, paired, this.cachedHeldAction(receipt)); }
        catch (_) { if (this.owns(generation, scope)) this.error = 'cache_unavailable'; }
      }
    } catch (error) {
      if (current()) this.failure(error as Error);
    } finally {
      this.heldActions = this.heldActions.filter((item: string) => item !== key);
      if (this.owns(generation, scope) && this.repository === repo) this.queueRefresh();
    }
  }
  canApprove(): boolean { return this.connected && this.capabilities().canApprove && this.decisionsReady(); }
  canApproveSession(row: ResourceRow): boolean {
    const session=this.sessions.find((item: ResourceRow) => item.machine===row.machine && (item.session || item.id)===(row.session || row.id));
    return this.connected && row.manager===this.profile.manager && (!row.group || row.group===this.profile.id) &&
      !!session && this.capabilities(session).canApprove;
  }
  canInterrupt(): boolean { return this.connected && this.capabilities().canInterrupt; }
  canInterruptSession(row: ResourceRow): boolean {
    if (!this.connected || row.kind !== 'session') return false;
    if (row.group && row.group !== this.profile.id) return false;
    if (row.manager && row.manager !== this.profile.manager) return false;
    const session = this.sessions.find((item: ResourceRow) =>
      item.machine === row.machine && (item.session || item.id) === (row.session || row.id)
    );
    return this.capabilities(session || row).canInterrupt;
  }
  private serializeDraftContexts(refs: Record<string,Object>[]): string {
    if (!refs.length) return '';
    return JSON.stringify(refs.length===1 ? refs[0] : Wire.payload(['contexts'],[refs]));
  }
  draftMessageQuotes(): Record<string,Object>[] {
    if (!this.sourceContext) return [];
    return this.draftContexts(this.sourceContext).filter((ref:Record<string,Object>) => ['message','quote'].includes(Wire.text(ref,'kind')));
  }
  otherContextTitle(): string {
    if (!this.sourceContext) return '';
    return this.draftContexts(this.sourceContext).filter((ref:Record<string,Object>) => !['message','quote','forward'].includes(Wire.text(ref,'kind')))
      .map((ref:Record<string,Object>) => Wire.text(ref,'title')).filter((title:string) => !!title).join(' · ');
  }
  removeMessageQuote(ref: Record<string,Object>): void {
    const before=this.draftContexts(this.sourceContext),key=JSON.stringify(ref);
    const index=before.findIndex((candidate:Record<string,Object>) => JSON.stringify(candidate)===key &&
      ['message','quote'].includes(Wire.text(candidate,'kind')));
    if (index<0) return;
    before.splice(index,1); this.sourceContext=this.serializeDraftContexts(before);
    this.draftRevision=util.generateRandomUUID(); void this.persistDraft().catch(() => this.error='cache_unavailable');
  }
  removeOtherContexts(): void {
    const keep=this.draftContexts(this.sourceContext).filter((ref:Record<string,Object>) => ['message','quote','forward'].includes(Wire.text(ref,'kind')));
    if (!keep.length) { this.removeContext(); return; }
    this.sourceContext=this.serializeDraftContexts(keep); this.draftRevision=util.generateRandomUUID();
    void this.persistDraft().catch(() => this.error='cache_unavailable');
  }
  contextTitle(): string {
    if (!this.sourceContext) return ''; const data = Wire.parse(this.sourceContext);
    return Wire.rows(data, 'contexts').map((item: Record<string, Object>) => Wire.text(item, 'title')).join(' · ') || Wire.text(data, 'title');
  }
  shortPath(path: string): string { return path.replace(/\\/g, '/').split('/').filter((part: string) => !!part).slice(-2).join('/'); }
  machineName(id: string): string { return this.machines.find((row: ResourceRow) => row.id === id)?.title || id; }
  async reconcileWrites(): Promise<void> {
    const repo = this.repo(), generation = this.generation, scope = this.scope();
    if (!this.features.includes('client_ops')) return;
    const records = (repo.mutations?.blocked() || []).slice();
    for (const entry of await new ChatDraftStore(this.local).list(scope)) if (['pending', 'unknown'].includes(entry.state) && !records.some((record: MutationRecord) => record.id === entry.id) && !this.activeMessages.has(entry.id)) { const record = new MutationRecord(); record.id = entry.id; record.state = entry.state; records.push(record); }
    for (const record of records) {
      try {
        const result = await repo.call('/api/v1/ops/' + segment(record.id));
        if (!this.owns(generation, scope)) return;
        const state = Wire.text(result, 'state');
        if (['done', 'failed', 'unknown', 'pending'].includes(state)) {
          if (repo.mutations?.blocked().some((item: MutationRecord) => item.id === record.id)) await repo.mutations?.finish(record, state);
          const entry = (await new ChatDraftStore(this.local).list(scope)).find((item: ChatOutboxEntry) => item.id === record.id);
          if (entry) { if (state === 'done') await this.completeLocalDelivery(entry, scope); else await new ChatDraftStore(this.local).settle(scope, entry.id, state); }
        }
      } catch (_) { /* A missing receipt never proves that a write did not execute. */ }
    }
    if (this.owns(generation, scope)) { this.unresolvedWrites = repo.mutations?.blocked() || [];
      await this.restoreOutbox(generation, scope); }
  }
  async acknowledgeWrite(id: string): Promise<void> {
    const repo = this.repo(), generation = this.generation, scope = this.scope();
    if (this.features.includes('client_ops')) {
      try {
        const result = await repo.call('/api/v1/ops/' + segment(id)); if (!this.owns(generation, scope)) return;
        const state = Wire.text(result, 'state'), record = repo.mutations?.blocked().find((item: MutationRecord) => item.id === id);
        if (state !== 'unknown') {
          if (record && ['done', 'failed', 'pending'].includes(state)) await repo.mutations?.finish(record, state);
          const entry = (await new ChatDraftStore(this.local).list(scope)).find((item: ChatOutboxEntry) => item.id === id);
          if (entry && state === 'done') await this.completeLocalDelivery(entry, scope, repo);
          else if (entry && ['failed', 'pending'].includes(state)) await new ChatDraftStore(this.local).settle(scope, id, state);
          if (this.owns(generation, scope)) { this.unresolvedWrites = repo.mutations?.blocked() || []; await this.restoreOutbox(generation, scope); } return;
        }
      } catch (error) {
        const localOnly = !(repo.mutations?.blocked().some((record: MutationRecord) => record.id === id));
        if (!(error instanceof HttpFailure && error.status === 404 && localOnly)) { if (this.owns(generation, scope)) this.failure(error as Error); return; }
        if (!this.owns(generation, scope) || !this.canCheckLocalMessage(id)) return;
      }
    }
    await repo.mutations?.acknowledge(id);
    if (repo.mutations?.blocked().some((record: MutationRecord) => record.id === id)) return;
    await new ChatDraftStore(this.local).settle(scope, id, 'checked');
    if (this.owns(generation, scope)) { this.unresolvedWrites = repo.mutations?.blocked() || []; await this.restoreOutbox(generation, scope); }
  }
  private sshViewKey(hub: string): string { return 'view:' + scopeKey({ groupId: hub, managerId: '' }) + ':ssh-tunnel'; }
  private viewKey(key: string): string {
    if (key === 'pickIncludeQuestions') return 'device:pickIncludeQuestions';
    if (key === 'forwardTargets') return 'view:'+scopeKey({groupId:this.profile.id,managerId:''})+':forwardTargets';
    if (key === 'agent-hub-mode') return 'device:agent-hub-mode';
    if (key === 'usage.hiddenProviders') return 'device:usage.hiddenProviders';
    return key === 'ssh-tunnel' ? this.sshViewKey(this.profile.id) : 'view:' + this.scope() + ':' + key;
  }
  viewRevision(key: string): number { return this.local.revision(this.viewKey(key)); }
  /** Outcome bookkeeping stays on the admitted scope, even after navigation; Forget retires the captured epoch. */
  captureView(key: string): ClientViewLease {
    return this.captureViewPath(this.viewKey(key));
  }
  async savedRenameTargets(group:string, manager:string, machine:string, source:string, requestId:string):Promise<string[]> {
    const scope=this.scope(), generation=this.connectionGeneration();
    const root=this.viewKey('worktree-request:');
    const prefix=JSON.stringify([scope,group,manager,machine,source]).slice(0,-1)+',';
    const keys=await this.local.keys(root+prefix,requestId);
    if (!this.owns(generation,scope)) throw new Error('request_updated');
    return keys.filter((key:string)=>key.startsWith(root+prefix) && key.endsWith(',"rename"]')).map((key:string)=>key.slice(root.length));
  }
  private captureViewPath(path: string): ClientViewLease {
    const local = this.local, epoch = local.revision(path);
    const valid = (): boolean => local.accepts(path, epoch);
    return { storageKey:path, epoch: epoch, valid: valid,
      read: async (): Promise<string> => {
        if (!valid()) throw new Error('request_updated');
        const result = await local.get(path);
        if (!valid()) throw new Error('request_updated');
        return result;
      },
      write: async (value: string): Promise<void> => {
        if (!valid()) throw new Error('request_updated');
        await local.put(path, value, epoch);
        if (!valid()) throw new Error('request_updated');
      },
      clearIfUnchanged: async (expected: string): Promise<void> => {
        if (valid()) await local.remove(path, epoch, expected);
      } };
  }
  async saveView(key: string, value: string, expectedEpoch: number = this.viewRevision(key)): Promise<void> { await this.local.put(this.viewKey(key), value, expectedEpoch); }
  async loadView(key: string): Promise<string> { return this.local.get(this.viewKey(key)); }
  removeContext():void {
    const previous=this.sourceContext,target=this.target();this.sourceContext='';this.draftRevision=util.generateRandomUUID();
    this.persistDraft().then(()=>new ChatDraftStore(this.local).discardForwardMedia(target,previous,'')).catch(()=>this.error='failed');
  }
  private autoRenameKey(row: ResourceRow): string {
    const sid = row.session || row.id;
    return JSON.stringify([this.scope(), this.connectionGeneration(), row.machine, sid]);
  }
  canAutoRenameAgent(row: ResourceRow): boolean {
    if (!this.connected || row.kind !== 'session' || Wire.bool(row.data(), 'assistant')) return false;
    if (!this.demo && !this.features.includes('agent_autoname')) return false;
    const sid = row.session || row.id;
    return !!row.machine && !!sid;
  }
  isAutoRenamingRow(row: ResourceRow): boolean {
    if (!row || row.kind !== 'session') return false;
    return this.namingAgentKeys.includes(this.autoRenameKey(row));
  }
  async autoRenameAgent(row: ResourceRow): Promise<string> {
    if (!this.canAutoRenameAgent(row)) return '';
    const key = this.autoRenameKey(row);
    if (this.namingAgentKeys.includes(key)) return '';
    const machine = row.machine, sid = row.session || row.id;
    const path = '/api/v1/sessions/' + segment(machine) + '/' + segment(sid) + '/autoname';
    if (this.operationBlocked(path)) return 'pending_action';
    const generation = this.generation, scope = this.scope();
    this.namingAgentKeys = this.namingAgentKeys.concat([key]);
    try {
      await this.repo().call(path, 'POST', {}, '', () => this.owns(generation, scope));
      if (!this.owns(generation, scope)) return '';
      if (this.demo) await this.refresh();
      return '';
    } catch (error) {
      if (!this.owns(generation, scope)) return '';
      const msg = error instanceof Error ? error.message : 'failed';
      return msg || 'failed';
    } finally {
      this.namingAgentKeys = this.namingAgentKeys.filter((entry: string) => entry !== key);
    }
  }
  async createAgent(prompt: string, machine: ResourceRow, directory: string, launcher: string, todoId: string = '', admit?: () => boolean, workspaceId: string = '', host: string = '', model: string = '', effort: string = '', tabName: string = ''): Promise<ResourceRow> {
    if (machine.manager !== this.profile.manager) throw new Error('request_updated');
    const generation = this.generation, scope = this.scope(), repo = this.repo();
    const row = await repo.createAgent(prompt, machine, directory, launcher, util.generateRandomUUID(), todoId, (): boolean => this.owns(generation, scope) && (!admit || admit()), workspaceId, host, model, effort, tabName);
    if (!this.owns(generation, scope)) throw new Error('request_updated');
    try { await this.refresh(); } catch (_) { if (this.owns(generation, scope)) this.error = 'failed'; }
    if (!this.owns(generation, scope)) throw new Error('request_updated'); row.group = this.profile.id; return row;
  }
  async saveKnowledge(row: ResourceRow, tags: string, importance: string): Promise<boolean> {
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (row.manager !== this.profile.manager) return false;
    try {
      const repo = this.repo(); await repo.saveKnowledge(row, tags.split(',').map((value: string) => value.trim()).filter((value: string) => !!value), importance);
      if (!this.owns(generation, scope)) return false;
      await this.listResources('knowledge'); return this.owns(generation, scope) && this.target() === target;
    } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); return false; }
  }
  async searchKnowledge(query: string): Promise<void> {
    const generation = this.generation, scope = this.scope(), kind = this.knowledgeKind;
    try { const repo = this.repo(), rows = await repo.knowledge(kind, query); if (this.owns(generation, scope) && this.knowledgeKind === kind) this.resourceRows = rows; }
    catch (error) { if (this.owns(generation, scope) && this.knowledgeKind === kind) this.failure(error as Error); }
  }
  async replyTo(row: ResourceRow, text: string): Promise<boolean> {
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (row.manager !== this.profile.manager) return false;
    try {
      const repo = this.repo(), next = await repo.reply(row, text, util.generateRandomUUID());
      if (!this.owns(generation, scope)) return false;
      if (this.target() === target && this.selected.key() === row.key()) this.selected = next;
      this.inbox = this.inbox.map((item: ResourceRow) => item.key() === row.key() ? next : item); return true;
    } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); return false; }
  }
  async processInbox(row: ResourceRow): Promise<void> {
    const generation = this.generation, scope = this.scope(), target = this.target();
    if (row.manager !== this.profile.manager) return;
    try {
      const repo = this.repo(), next = await repo.processInbox(row);
      if (!this.owns(generation, scope)) return;
      if (this.target() === target && this.selected.key() === row.key()) this.selected = next;
      this.inbox = this.inbox.map((item: ResourceRow) => item.key() === row.key() ? next : item);
    } catch (error) { if (this.owns(generation, scope) && this.target() === target) this.failure(error as Error); }
  }
  private refreshDemo(): void {
    const demo = this.demoRepository; if (!this.demo || !demo) return;
    const roster = demo.roster(); this.features = roster.features; this.mainId = roster.mainId;
    this.sessions = roster.sessions; this.sessionsReady = true; this.machines = roster.machines; this.conversations = roster.conversations; this.mainMachineId = roster.conversations.find((row: ResourceRow) => row.id === roster.mainId)?.machine || '';
    if (roster.leaders.length > 0 || this.sidebarConfig !== '') {
      this.acceptSidebarConfig(Wire.payload(['main', 'main_machine_id', 'leaders'], [roster.mainId, this.mainMachineId, roster.leaders]));
    }
    this.todos = roster.todos; this.knowledge = roster.knowledge; this.inbox = roster.inbox;
    this.managers = [new ResourceRow('demo', tr('local_demo'), 'manager')];
    if (this.selected.kind === 'session') { const current = this.sessions.find((row: ResourceRow) => row.id === this.selected.id && row.machine === this.selected.machine); if (current) this.selected = current; }
    this.directoryReady = true; this.directoryLoading = false; this.directoryUnavailable = 0; this.publishDirectory();
    if (!this.detached) nativeLiveView.syncFromModel(this).catch(() => {});
  }
  useDemo(): void {
    this.ssh.stop(); this.stopReconnect(); this.cancelDraftTimer(); this.closeTranscript(); this.releaseTerminal(); this.retireViews(); this.repository?.close(); this.generation++; this.selectionGeneration++;
    this.publishedImageLimits = {};
    this.namingAgentKeys = [];
    this.demo = true; this.connected = true; this.profile = new HubProfile('demo', tr('local_demo')); this.profile.hubId = 'demo'; this.profile.manager = 'demo';
    this.features = []; this.rememberedHubModules = null; this.sidebarConfig = '';
    this.useHerdrTabName = false; this.agentsDisplaySupported = false; this.agentsDisplaySaving = false; this.agentsDisplayRevision++;
    this.nextSuggestionsValue = true; this.nextSuggestionsLoaded = true; this.nextSuggestionsSaving = false; this.nextSuggestionsError = ''; this.nextSuggestionsRevision++;
    this.sendAttempts.clear(); this.feedbackChanged();
    this.demoRepository = new DemoRepository(this.local); this.repository = this.demoRepository;
    this.selected = new ResourceRow(); this.messages = []; this.revisionItems = []; this.clearPendingDecisions(); this.draft = ''; this.sourceContext = ''; this.draftRevision = ''; this.draftLoading = false;
    this.localMessages = []; this.renderedMessages = []; this.echoedMessages.clear(); this.unresolvedWrites = []; this.operationState = ''; this.resourceRows = []; this.detail = ''; this.reply = '';
    this.knowledgeKind = 'projects'; this.refreshDemo(); this.error = ''; this.section = 0;
  }
  releaseTerminal(): void { this.workspace?.hide(); this.terminalControl = false; }
  async background(): Promise<void> {
    this.foreground = false; if (!this.detached) nativeLiveView.setForeground(false, this.hasPushRoute(), this.demo); this.ssh.stop(); this.stopReconnect(); this.cancelDraftTimer(); this.closeTranscript(); this.clearPendingDecisions(); this.releaseTerminal();
    this.feedbackChanged();
    const repository = this.repository; repository?.close();
    const children = Array.from(this.views.values()).map((view: ClientModel): Promise<void> => view.background());
    await this.persistDraft(); await Promise.all(children);
  }
  activateForeground(): void { this.foreground = true; if (!this.detached) nativeLiveView.setForeground(true); this.feedbackChanged(); for (const view of this.views.values()) view.activateForeground(); }
  async foregrounded(): Promise<void> { if (this.retired) return; this.activateForeground(); await this.retryCleanup(); if (this.retired || !this.foreground) return; if (this.profile.id && !this.demo) await this.selectProfile(this.profile, true); await this.flushNotification(); }
  async shutdown(): Promise<void> { await this.background(); this.retireViews(); if (!this.detached) await this.local.close(); }
}

export const client = new ClientModel();
