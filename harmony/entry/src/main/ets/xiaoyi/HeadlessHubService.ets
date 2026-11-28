import { common } from '@kit.AbilityKit';
import { screenLock } from '@kit.BasicServicesKit';
import { i18n } from '@kit.LocalizationKit';
import { util } from '@kit.ArkTS';
import { clientLocale } from '../core/ClientLocale';
import { scopeKey } from '../core/Domain';
import type { HubAccess } from '../core/HubAccess';
import {
  XiaoYiInteraction,
  XiaoYiMachine,
  XiaoYiMuteRules,
  XiaoYiPushItem,
  XiaoYiSession,
  XiaoYiStatusSummary,
  XiaoYiTranscriptEntry,
  buildApprovalPayload,
  buildPushItem,
  enqueueOutboxItem,
  extractLatestAssistantAnswer,
  extractPublishedModules,
  formatStatusSummary,
  isRecordingModuleAllowed,
  parseStoredHubModules,
  pruneOutboxItems,
  selectLiveSession,
  selectMachine,
  selectPendingApprovals,
  serializeStoredHubModules,
  takeOutboxBatch
} from '../core/XiaoYiActions';
import { XiaoYiRoute, buildXiaoYiRouteUri, createXiaoYiRoute } from '../core/XiaoYiRoutes';
import { loadAgentSetups, RecentAgentSetup } from '../models/AgentLaunchChoices';
import { intakeAmount, intakeRoute } from '../models/HealthIntake';
import { HubProfile, Wire } from '../models/Wire';
import { CredentialVault } from '../platform/CredentialVault';
import { EndpointConfig, HttpFailure, HubTransport } from '../platform/HubTransport';
import { LocalStore } from '../platform/LocalStore';

const HEALTH_QUERY: string = '?space=health';
const MAX_A2A_CONTEXTS: number = 64;
const DEFAULT_ASK_TIMEOUT_MS: number = 24000;
const HEALTH_PROBE_TIMEOUT_MS: number = 2500;
const POLL_INTERVAL_MS: number = 900;

export class HeadlessHubFailure extends Error {
  readonly code: string;
  readonly host: string;
  readonly target: string;
  constructor(code: string, host: string = '', target: string = '') {
    super(code);
    this.name = 'HeadlessHubFailure';
    this.code = code;
    this.host = host;
    this.target = target;
  }
}

export interface HeadlessAskOptions {
  conversationId?: string;
  machineId?: string;
  timeoutMs?: number;
  onProgress?: (text: string, done: boolean) => void;
  onTarget?: (machineId: string, sessionId: string) => void;
  isCancelled?: () => boolean;
}

export interface HeadlessAskResult {
  answer: string;
  pending: boolean;
  machineId: string;
  sessionId: string;
  profileId: string;
  managerId: string;
  deepLinkUri: string;
  askUserPrompt: string;
}

export interface HeadlessSendTaskResult {
  machineId: string;
  machineName: string;
  sessionId: string;
  sessionTitle: string;
  createdNew: boolean;
  deepLinkUri: string;
}

export interface HeadlessApproveResult {
  approved: boolean;
  needsScreen: boolean;
  decisionId: string;
  summary: string;
  reason: string;
  deepLinkUri: string;
}

export interface HeadlessTodoResult {
  id: string;
  title: string;
  deepLinkUri: string;
}

export interface HeadlessWaterResult {
  recordedMl: number;
  todayMl: number;
  goalMl: number;
  deepLinkUri: string;
}

export interface HeadlessPushResult {
  status: 'sent' | 'queued';
  newCount: number;
  hasDestination: boolean;
  queuedCount: number;
}

export interface HeadlessKnowledgeHit {
  path: string;
  line: string;
}

export interface HeadlessContextBinding {
  machineId: string;
  sessionId: string;
  updatedAt: number;
}

export interface HeadlessRecordingStartResult {
  allowed: boolean;
  deepLinkUri: string;
}

export interface HeadlessDiagnosticReport {
  paired: boolean;
  profileName: string;
  hubHost: string;
  credentialReadable: boolean;
  sshTunnelOnly: boolean;
  hubReachable: boolean;
  recordingAllowed: boolean;
  onlineMachines: number;
  totalMachines: number;
  errorCode: string;
}

type SshRouteMode = 'none' | 'fallback' | 'required';

interface ActiveHubConnection {
  profile: HubProfile;
  scope: string;
  host: string;
  sshFallback: boolean;
  transport: HubTransport;
}

function segment(value: string): string {
  return encodeURIComponent(value);
}

function extractHostLabel(baseUrl: string): string {
  const match = /^https?:\/\/([^/?#]+)/i.exec(baseUrl || '');
  return match ? match[1] : '';
}

function mapHttpError(error: Error, host: string, target: string = ''): HeadlessHubFailure {
  if (error instanceof HeadlessHubFailure) return error;
  if (error instanceof HttpFailure) {
    if (error.status === 401 || error.status === 403) return new HeadlessHubFailure('xiaoyi_auth_expired', host, target);
    if (error.status === 410) return new HeadlessHubFailure('xiaoyi_already_handled', host, target);
    if (error.status === 404) return new HeadlessHubFailure('xiaoyi_hub_update_required', host, target);
    if (error.status === 429) return new HeadlessHubFailure('xiaoyi_rate_limited', host, target);
    if (error.status === 502 || error.status === 503 || error.status === 504) {
      return new HeadlessHubFailure('xiaoyi_offline', host, target);
    }
    if (error.code === 'timeout' || error.message === 'timeout') {
      return new HeadlessHubFailure('xiaoyi_timeout', host, target);
    }
    if (error.status === 0) return new HeadlessHubFailure('xiaoyi_offline', host, target);
    return new HeadlessHubFailure(error.code || 'xiaoyi_failed', host, target);
  }
  const msg = error.message || '';
  if (msg.includes('timeout')) return new HeadlessHubFailure('xiaoyi_timeout', host, target);
  if (msg === 'offline' || msg.includes('network') || msg.includes('connect')) {
    return new HeadlessHubFailure('xiaoyi_offline', host, target);
  }
  return new HeadlessHubFailure('xiaoyi_failed', host, target);
}

function isTransientPushError(error: Error): boolean {
  if (error instanceof HttpFailure) {
    return error.status === 0 || error.status === 502 || error.status === 503 || error.status === 504;
  }
  return error instanceof HeadlessHubFailure && (error.code === 'xiaoyi_offline' || error.code === 'xiaoyi_timeout');
}

function sleepMs(ms: number): Promise<void> {
  return new Promise<void>((resolve) => {
    setTimeout(resolve, ms);
  });
}

function decodeMachines(rows: Record<string, Object>[]): XiaoYiMachine[] {
  return rows.map((row: Record<string, Object>): XiaoYiMachine => {
    const id = Wire.text(row, 'id');
    const trust = Wire.text(row, 'trust');
    const trusted = trust !== 'pending' && !Wire.bool(row, 'pending_confirm') && !Wire.bool(row, 'pending');
    return {
      id: id,
      name: Wire.text(row, 'name', Wire.text(row, 'title', id)),
      online: Wire.bool(row, 'online', true),
      trusted: trusted,
      manager: Wire.text(row, 'manager', Wire.text(row, 'manager_id'))
    };
  });
}

function decodeSessions(rows: Record<string, Object>[]): XiaoYiSession[] {
  return rows.map((row: Record<string, Object>): XiaoYiSession => {
    const id = Wire.text(row, 'session_id', Wire.text(row, 'id'));
    const caps = Wire.object(row, 'capabilities');
    const hasSource = !!Wire.text(row, 'source_kind');
    const readOnly = Wire.bool(row, 'read_only') || Wire.text(row, 'source_kind') === 'identity_conflict';
    const canSend = !readOnly && (!hasSource || Wire.bool(caps, 'send'));
    return {
      id: id,
      machineId: Wire.text(row, 'machine_id', Wire.text(row, 'machine')),
      title: Wire.text(row, 'title', Wire.text(row, 'name', id)),
      status: Wire.text(row, 'status'),
      state: Wire.text(row, 'state', Wire.text(row, 'status')),
      updatedAt: Wire.text(row, 'updated_at'),
      assistant: Wire.bool(row, 'assistant'),
      quiet: Wire.bool(row, 'quiet') || !!Wire.text(Wire.object(row, 'todo'), 'id'),
      muted: Wire.bool(row, 'muted'),
      spaceName: Wire.text(row, 'space_name'),
      unreadCount: Wire.number(row, 'unread_count', Wire.number(row, 'unread')),
      canSend: canSend
    };
  });
}

function decodeInteractions(rows: Record<string, Object>[]): XiaoYiInteraction[] {
  return rows.map((row: Record<string, Object>): XiaoYiInteraction => {
    const nested = Wire.object(row, 'interaction');
    const data = Object.keys(nested).length > 0 ? nested : row;
    const fields = Wire.rows(data, 'fields');
    const hasRequiredFields = fields.some((f: Record<string, Object>): boolean => !Wire.bool(f, 'optional'));
    return {
      id: Wire.text(data, 'id', Wire.text(row, 'id')),
      machineId: Wire.text(data, 'machine_id', Wire.text(row, 'machine_id')),
      sessionId: Wire.text(data, 'session_id', Wire.text(row, 'session_id')),
      kind: Wire.text(data, 'kind', Wire.text(row, 'kind')),
      status: Wire.text(row, 'status', Wire.text(data, 'status', 'open')),
      summary: Wire.text(data, 'summary', Wire.text(data, 'plan', Wire.text(data, 'prompt', Wire.text(data, 'question')))),
      toolName: Wire.text(data, 'tool_name'),
      createdAt: Wire.text(data, 'created_at', Wire.text(row, 'created_at')),
      delegating: Wire.bool(data, 'delegating'),
      muted: Wire.bool(data, 'muted') || Wire.bool(row, 'muted'),
      quiet: Wire.bool(data, 'quiet') || Wire.bool(row, 'quiet'),
      planThen: Wire.text(data, 'plan_then'),
      hasRequiredFields: hasRequiredFields
    };
  });
}

function decodeTranscriptEntries(rows: Record<string, Object>[]): XiaoYiTranscriptEntry[] {
  return rows.map((row: Record<string, Object>): XiaoYiTranscriptEntry => {
    return {
      seq: Wire.number(row, 'seq'),
      kind: Wire.text(row, 'kind'),
      role: Wire.text(row, 'role'),
      text: Wire.text(row, 'text')
    };
  });
}

function sessionRouteUri(profileId: string, managerId: string, machineId: string, sessionId: string): string {
  const route: XiaoYiRoute = createXiaoYiRoute('session');
  route.profile = profileId;
  route.manager = managerId;
  route.machine = machineId;
  route.session = sessionId;
  return buildXiaoYiRouteUri(route);
}

/**
 * UI-independent Hub service usable from EntryAbility, AgentExtensionAbility,
 * AgentUIExtensionAbility, and InsightIntentEntryExecutor.
 */
export class HeadlessHubService {
  private readonly context: common.Context;
  private readonly local: LocalStore;
  private readonly vault: CredentialVault;
  private opened: boolean = false;

  constructor(context: common.Context, dbName: string = 'persona.db') {
    this.context = context;
    this.local = new LocalStore(dbName);
    this.vault = new CredentialVault(context);
  }

  private async ensureOpen(): Promise<void> {
    if (this.opened) return;
    await this.local.open(this.context);
    this.opened = true;
  }

  async close(): Promise<void> {
    if (!this.opened) return;
    this.opened = false;
    try {
      await this.local.close();
    } catch (_) {
      // Ignore store close failures during extension teardown.
    }
  }

  /** Fail-closed screen-lock check: returns true if locked or if lock state cannot be verified. */
  async isDeviceLocked(): Promise<boolean> {
    try {
      return await screenLock.isScreenLocked();
    } catch (_) {
      return true;
    }
  }

  effectiveLocale(): string {
    try {
      return clientLocale(i18n.getSystemLanguage());
    } catch (_) {
      return 'en';
    }
  }

  private async loadActiveProfile(requestedProfileId: string = ''): Promise<HubProfile> {
    await this.ensureOpen();
    const saved = Wire.parse(await this.local.get('profiles'));
    const profiles = Wire.rows(saved, 'profiles').map((item: Record<string, Object>): HubProfile => {
      const profile = new HubProfile(Wire.text(item, 'id'), Wire.text(item, 'name'), Wire.text(item, 'baseUrl'));
      profile.addresses = Wire.strings(item, 'addresses');
      profile.hubId = Wire.text(item, 'hubId');
      profile.pin = Wire.text(item, 'pin');
      profile.manager = Wire.text(item, 'manager');
      return profile;
    });
    const activeId = requestedProfileId || Wire.text(saved, 'active');
    const selected = profiles.find((p: HubProfile): boolean => p.id === activeId) || (requestedProfileId ? undefined : profiles[0]);
    if (!selected || !selected.id || !selected.baseUrl) {
      throw new HeadlessHubFailure('xiaoyi_not_paired');
    }
    return selected;
  }

  private hubModulesKey(hubId: string): string {
    return 'xiaoyi:' + scopeKey({ groupId: hubId, managerId: '' }) + ':hub-modules';
  }

  private async loadStoredHubModules(hubId: string): Promise<string[] | null> {
    if (!hubId) return null;
    try {
      return parseStoredHubModules(await this.local.get(this.hubModulesKey(hubId)));
    } catch (_) {
      return null;
    }
  }

  private async saveStoredHubModules(hubId: string, published: string[] | null): Promise<void> {
    if (!hubId) return;
    try {
      await this.local.put(this.hubModulesKey(hubId), serializeStoredHubModules(published));
    } catch (_) {
      // Non-fatal cache update.
    }
  }

  /**
   * Fail-closed recording module check matching iOS `RecordingModule.allowance` (#15978 / #15985):
   * prefers live `GET /api/v1/health` `features` when returned by the Hub and falls back to remembered
   * per-Hub WS `hello` modules (`xiaoyi:<scope>:hub-modules`) when offline or over a public URL.
   */
  async isRecordingModuleAllowed(requestedProfileId: string = ''): Promise<boolean> {
    let profile: HubProfile;
    try {
      profile = await this.loadActiveProfile(requestedProfileId);
    } catch (_) {
      return true;
    }
    const stored = await this.loadStoredHubModules(profile.id);
    const sshMode = await this.sshRouteMode(profile.id);
    if (sshMode === 'required') {
      return isRecordingModuleAllowed(undefined, stored);
    }
    let conn: ActiveHubConnection | undefined = undefined;
    try {
      conn = await this.openConnection(profile.id, '');
    } catch (_) {
      return isRecordingModuleAllowed(undefined, stored);
    }
    let timerId: number = -1;
    try {
      const timeoutPromise = new Promise<Record<string, Object> | undefined>((resolve) => {
        timerId = setTimeout(() => resolve(undefined), HEALTH_PROBE_TIMEOUT_MS);
      });
      const probePromise = this.requestJson(conn, '/api/v1/health').catch((): Record<string, Object> | undefined => undefined);
      const health = await Promise.race<Record<string, Object> | undefined>([probePromise, timeoutPromise]);
      if (health) {
        const liveFeatures = Wire.strings(health, 'features');
        if (liveFeatures.length > 0) {
          await this.saveStoredHubModules(profile.id, extractPublishedModules(liveFeatures));
          return isRecordingModuleAllowed(liveFeatures);
        }
      }
      return isRecordingModuleAllowed(undefined, stored);
    } finally {
      if (timerId !== -1) clearTimeout(timerId);
      conn.transport.close();
    }
  }

  /** Throws `xiaoyi_recording_module_off` when the Hub's recording module is off. */
  async assertRecordingModuleAllowed(requestedProfileId: string = ''): Promise<void> {
    const allowed = await this.isRecordingModuleAllowed(requestedProfileId);
    if (!allowed) {
      let host = '';
      try {
        host = extractHostLabel((await this.loadActiveProfile(requestedProfileId)).baseUrl);
      } catch (_) {}
      throw new HeadlessHubFailure('xiaoyi_recording_module_off', host);
    }
  }

  /** Verify recording allowance and return the foreground auto-start route when allowed. */
  async prepareRecordingStart(requestedProfileId: string = ''): Promise<HeadlessRecordingStartResult> {
    await this.assertRecordingModuleAllowed(requestedProfileId);
    const route = createXiaoYiRoute('recording');
    route.autoStart = true;
    try {
      const profile = await this.loadActiveProfile(requestedProfileId);
      route.profile = profile.id;
    } catch (_) {}
    return { allowed: true, deepLinkUri: buildXiaoYiRouteUri(route) };
  }

  private async sshRouteMode(hubId: string): Promise<SshRouteMode> {
    const sshKey = 'view:' + scopeKey({ groupId: hubId, managerId: '' }) + ':ssh-tunnel';
    const raw = await this.local.get(sshKey);
    if (!raw) return 'none';
    try {
      const parsed = Wire.parse(raw);
      if (!Wire.bool(parsed, 'enabled')) return 'none';
      const policy = Wire.text(parsed, 'routePolicy', 'legacy');
      return policy === 'fallback' ? 'fallback' : 'required';
    } catch (_) {
      return 'none';
    }
  }

  private async openConnection(requestedProfileId: string = '', managerOverride?: string): Promise<ActiveHubConnection> {
    const profile = await this.loadActiveProfile(requestedProfileId);
    const host = extractHostLabel(profile.baseUrl);
    const sshMode = await this.sshRouteMode(profile.id);
    if (sshMode === 'required') {
      throw new HeadlessHubFailure('xiaoyi_ssh_open_app', host);
    }
    let access: HubAccess | undefined;
    let token: string | null = null;
    try {
      token = await this.vault.get(profile.id, (restored: HubAccess | undefined) => {
        access = restored;
      });
    } catch (_) {
      throw new HeadlessHubFailure('xiaoyi_auth_expired', host);
    }
    if (!token) {
      throw new HeadlessHubFailure('xiaoyi_auth_expired', host);
    }
    const manager = managerOverride !== undefined ? managerOverride : profile.manager;
    const config = new EndpointConfig(
      profile.baseUrl,
      profile.hubId || profile.id,
      token,
      profile.pin,
      manager,
      profile.addresses,
      undefined,
      access
    );
    return {
      profile: profile,
      scope: scopeKey({ groupId: profile.id, managerId: manager }),
      host: host,
      sshFallback: sshMode === 'fallback',
      transport: new HubTransport(config)
    };
  }

  private async requestJson(
    conn: ActiveHubConnection,
    path: string,
    method: string = 'GET',
    body?: Record<string, Object>,
    opId: string = ''
  ): Promise<Record<string, Object>> {
    try {
      const payload = body !== undefined ? JSON.stringify(body) : '';
      const raw = await conn.transport.request(path, method, payload, opId);
      return Wire.parse(raw);
    } catch (error) {
      const mapped = mapHttpError(error as Error, conn.host);
      if (conn.sshFallback && mapped.code === 'xiaoyi_offline') {
        throw new HeadlessHubFailure('xiaoyi_ssh_open_app', conn.host);
      }
      throw mapped;
    }
  }

  private async readMutes(conn: ActiveHubConnection): Promise<XiaoYiMuteRules> {
    try {
      const raw = await this.requestJson(conn, '/api/v1/mutes');
      return {
        spaces: Wire.strings(raw, 'spaces'),
        sessions: Wire.strings(raw, 'sessions'),
        allowed: Wire.strings(raw, 'allowed')
      };
    } catch (_) {
      return { spaces: [], sessions: [], allowed: [] };
    }
  }

  async listMachines(): Promise<XiaoYiMachine[]> {
    const conn = await this.openConnection();
    try {
      const res = await this.requestJson(conn, '/api/v1/machines');
      return decodeMachines(Wire.rows(res, 'machines'));
    } finally {
      conn.transport.close();
    }
  }

  async listActiveSessions(): Promise<XiaoYiSession[]> {
    const conn = await this.openConnection();
    try {
      const res = await this.requestJson(conn, '/api/v1/sessions');
      return decodeSessions(Wire.rows(res, 'sessions')).filter((s: XiaoYiSession): boolean =>
        !s.assistant && s.status.toLowerCase() !== 'offline' && s.state.toLowerCase() !== 'offline'
      );
    } finally {
      conn.transport.close();
    }
  }

  async checkStatus(): Promise<XiaoYiStatusSummary> {
    const conn = await this.openConnection();
    try {
      const sessionsRes = await this.requestJson(conn, '/api/v1/sessions');
      const sessions = decodeSessions(Wire.rows(sessionsRes, 'sessions'));
      const interactionsRes = await this.requestJson(conn, '/api/v1/interactions');
      const interactions = decodeInteractions(Wire.rows(interactionsRes, 'interactions'));
      const mutes = await this.readMutes(conn);
      const pending = selectPendingApprovals(interactions, sessions, mutes);
      let unreadPersonal = 0;
      try {
        const personalRes = await this.requestJson(conn, '/api/v1/agents/sessions');
        const personalRows = Wire.rows(personalRes, 'sessions');
        for (let i = 0; i < personalRows.length; i++) {
          if (Wire.number(personalRows[i], 'unread_count', Wire.number(personalRows[i], 'unread')) > 0) {
            unreadPersonal++;
          }
        }
      } catch (_) {
        // Optional endpoint on older hubs.
      }
      const summary = formatStatusSummary(sessions, pending, unreadPersonal, this.effectiveLocale());
      summary.profileId = conn.profile.id;
      summary.managerId = conn.profile.manager;
      return summary;
    } finally {
      conn.transport.close();
    }
  }

  private async resolveMainConversation(conn: ActiveHubConnection): Promise<string[]> {
    const assistantRes = await this.requestJson(conn, '/api/v1/assistant');
    const mainId = Wire.text(assistantRes, 'main');
    const mainMachine = Wire.text(assistantRes, 'main_machine_id', Wire.text(assistantRes, 'machine_id'));
    const conversations = Wire.rows(assistantRes, 'conversations');
    const mainRow = conversations.find((c: Record<string, Object>): boolean =>
      Wire.text(c, 'id', Wire.text(c, 'session_id')) === mainId
    );
    if (mainId && (mainMachine || (mainRow && Wire.text(mainRow, 'machine_id')))) {
      return [mainMachine || Wire.text(mainRow!, 'machine_id'), mainId];
    }
    const opCreate = util.generateRandomUUID().toLowerCase();
    const created = await this.requestJson(
      conn,
      '/api/v1/assistant/conversations',
      'POST',
      Wire.payload(['main', 'prompt'], [true, '']),
      opCreate
    );
    const sessionObj = Wire.object(created, 'session');
    const sessionId = Wire.text(sessionObj, 'id', Wire.text(sessionObj, 'session_id'));
    const machineId = Wire.text(sessionObj, 'machine_id', Wire.text(sessionObj, 'machine'));
    return [machineId, sessionId];
  }

  async askPersona(question: string, options?: HeadlessAskOptions): Promise<HeadlessAskResult> {
    const text = question.trim();
    if (!text) throw new HeadlessHubFailure('xiaoyi_nothing_to_send');
    const conn = await this.openConnection();
    try {
      let machineId = options?.machineId || '';
      let sessionId = options?.conversationId || '';
      const usedCachedBinding = !!(machineId && sessionId);

      if (!machineId || !sessionId) {
        const resolved = await this.resolveMainConversation(conn);
        machineId = resolved[0];
        sessionId = resolved[1];
      }
      if (!machineId || !sessionId) {
        throw new HeadlessHubFailure('xiaoyi_no_live_session', conn.host);
      }

      let itemsPathBase = '/api/v1/sessions/' + segment(machineId) + '/' + segment(sessionId) + '/items';
      let baselineSeq = 0;
      const readBaseline = async (): Promise<number> => {
        let seq = 0;
        try {
          const initialItems = await this.requestJson(conn, itemsPathBase + '?lines=20&fold_work=1&lazy_images=1');
          const entries = decodeTranscriptEntries(Wire.rows(initialItems, 'items'));
          for (let i = 0; i < entries.length; i++) {
            if (entries[i].seq > seq) seq = entries[i].seq;
          }
        } catch (_) {
          // Fresh conversation may have empty items.
        }
        return seq;
      };
      baselineSeq = await readBaseline();

      if (options?.onTarget) options.onTarget(machineId, sessionId);
      const opSend = util.generateRandomUUID().toLowerCase();
      try {
        await this.requestJson(
          conn,
          '/api/v1/sessions/' + segment(machineId) + '/' + segment(sessionId) + '/messages',
          'POST',
          Wire.payload(['text', 'client_msg_id'], [text, opSend]),
          opSend
        );
      } catch (sendError) {
        // Recover cleanly when a cached A2A contextId references an archived/deleted conversation (404).
        if (usedCachedBinding && sendError instanceof HeadlessHubFailure && sendError.code === 'xiaoyi_hub_update_required') {
          const fresh = await this.resolveMainConversation(conn);
          machineId = fresh[0];
          sessionId = fresh[1];
          if (!machineId || !sessionId) throw new HeadlessHubFailure('xiaoyi_no_live_session', conn.host);
          itemsPathBase = '/api/v1/sessions/' + segment(machineId) + '/' + segment(sessionId) + '/items';
          baselineSeq = await readBaseline();
          if (options?.onTarget) options.onTarget(machineId, sessionId);
          const retryOp = util.generateRandomUUID().toLowerCase();
          await this.requestJson(
            conn,
            '/api/v1/sessions/' + segment(machineId) + '/' + segment(sessionId) + '/messages',
            'POST',
            Wire.payload(['text', 'client_msg_id'], [text, retryOp]),
            retryOp
          );
        } else {
          throw sendError as Error;
        }
      }

      const timeoutMs = options?.timeoutMs ?? DEFAULT_ASK_TIMEOUT_MS;
      const deadline = Date.now() + timeoutMs;
      let latestText = '';
      let askUserPrompt = '';
      let finished = false;

      while (Date.now() < deadline) {
        if (options?.isCancelled && options.isCancelled()) {
          await this.interruptSessionWithConn(conn, machineId, sessionId);
          break;
        }
        await sleepMs(POLL_INTERVAL_MS);
        if (options?.isCancelled && options.isCancelled()) {
          await this.interruptSessionWithConn(conn, machineId, sessionId);
          break;
        }

        const pollRes = await this.requestJson(
          conn,
          itemsPathBase + '?after_seq=' + String(baselineSeq) + '&lines=80&fold_work=1&lazy_images=1'
        );
        const extracted = extractLatestAssistantAnswer(decodeTranscriptEntries(Wire.rows(pollRes, 'items')), baselineSeq);
        if (extracted.hasReply && extracted.text !== latestText) {
          latestText = extracted.text;
          if (options?.onProgress) options.onProgress(latestText, false);
        }

        // Check if assistant is still working or posted an ask_user question.
        try {
          const interRes = await this.requestJson(conn, '/api/v1/interactions');
          const inters = decodeInteractions(Wire.rows(interRes, 'interactions'));
          const pendingAsk = inters.find((item: XiaoYiInteraction): boolean =>
            item.machineId === machineId && item.sessionId === sessionId &&
            (item.status === 'open' || item.status === 'pending' || !item.status) &&
            (item.kind === 'question' || item.kind === 'permission' || item.kind === 'plan')
          );
          if (pendingAsk) {
            askUserPrompt = pendingAsk.summary || pendingAsk.toolName || pendingAsk.kind;
            finished = true;
            break;
          }
        } catch (_) {
          // Non-fatal during polling.
        }

        try {
          const sessionsRes = await this.requestJson(conn, '/api/v1/sessions');
          const sessions = decodeSessions(Wire.rows(sessionsRes, 'sessions'));
          const current = sessions.find((s: XiaoYiSession): boolean => s.machineId === machineId && s.id === sessionId);
          const stillWorking = current ? (current.state === 'working' || current.status === 'working' || current.status === 'running') : false;
          if (!stillWorking && extracted.hasReply) {
            finished = true;
            break;
          }
        } catch (_) {
          if (extracted.hasReply) {
            finished = true;
            break;
          }
        }
      }

      if (latestText && options?.onProgress && !(options?.isCancelled && options.isCancelled())) {
        options.onProgress(latestText, true);
      }

      return {
        answer: latestText,
        pending: !finished && !latestText,
        machineId: machineId,
        sessionId: sessionId,
        profileId: conn.profile.id,
        managerId: conn.profile.manager,
        deepLinkUri: sessionRouteUri(conn.profile.id, conn.profile.manager, machineId, sessionId),
        askUserPrompt: askUserPrompt
      };
    } finally {
      conn.transport.close();
    }
  }

  private async interruptSessionWithConn(conn: ActiveHubConnection, machineId: string, sessionId: string): Promise<void> {
    try {
      await this.requestJson(
        conn,
        '/api/v1/sessions/' + segment(machineId) + '/' + segment(sessionId) + '/interrupt',
        'POST',
        {}
      );
    } catch (_) {
      // Best-effort cancellation.
    }
  }

  async interruptSession(machineId: string, sessionId: string, requestedProfileId: string = '', managerOverride?: string): Promise<void> {
    if (!machineId || !sessionId) return;
    const conn = await this.openConnection(requestedProfileId, managerOverride);
    try {
      await this.interruptSessionWithConn(conn, machineId, sessionId);
    } finally {
      conn.transport.close();
    }
  }

  async sendAgentTask(
    prompt: string,
    machineName: string = '',
    sessionName: string = '',
    newSession: boolean = false
  ): Promise<HeadlessSendTaskResult> {
    const cleanPrompt = prompt.trim();
    if (!cleanPrompt) throw new HeadlessHubFailure('xiaoyi_nothing_to_send');
    const conn = await this.openConnection();
    try {
      const machinesRes = await this.requestJson(conn, '/api/v1/machines');
      const machines = decodeMachines(Wire.rows(machinesRes, 'machines'));
      const setupsRaw = await this.local.get('view:' + conn.scope + ':recent-agent-setups');
      const recentSetups: RecentAgentSetup[] = setupsRaw ? loadAgentSetups(setupsRaw) : [];
      const rememberedMachineId = recentSetups.length > 0 ? recentSetups[0].machine : '';

      const pickedMachine = selectMachine(machines, machineName, rememberedMachineId);
      if (!pickedMachine.ok || !pickedMachine.machine) {
        throw new HeadlessHubFailure(pickedMachine.errorCode, conn.host, pickedMachine.target);
      }
      const machine = pickedMachine.machine;
      const opId = util.generateRandomUUID().toLowerCase();

      if (newSession) {
        const setup = recentSetups.find((s: RecentAgentSetup): boolean => s.machine === machine.id && !!s.cwd && !!s.launcher);
        if (!setup) {
          throw new HeadlessHubFailure('xiaoyi_need_app_for_new_session', conn.host, machine.name || machine.id);
        }
        const body = Wire.payload(['machine_id', 'cwd', 'launcher', 'prompt'], [machine.id, setup.cwd, setup.launcher, cleanPrompt]);
        if (setup.model) body['model'] = setup.model;
        if (setup.effort) body['effort'] = setup.effort;
        const created = await this.requestJson(conn, '/api/v1/sessions', 'POST', body, opId);
        const sessionObj = Wire.object(created, 'session');
        const sessionId = Wire.text(sessionObj, 'id', Wire.text(sessionObj, 'session_id'));
        const title = Wire.text(sessionObj, 'title', sessionId);
        return {
          machineId: machine.id,
          machineName: machine.name || machine.id,
          sessionId: sessionId,
          sessionTitle: title,
          createdNew: true,
          deepLinkUri: sessionRouteUri(conn.profile.id, conn.profile.manager, machine.id, sessionId)
        };
      }

      const sessionsRes = await this.requestJson(conn, '/api/v1/sessions');
      const sessions = decodeSessions(Wire.rows(sessionsRes, 'sessions'));
      const pickedSession = selectLiveSession(sessions, machine.id, sessionName);
      if (!pickedSession.ok || !pickedSession.session) {
        throw new HeadlessHubFailure(pickedSession.errorCode, conn.host, pickedSession.target || machine.name);
      }
      const session = pickedSession.session;
      try {
        await this.requestJson(
          conn,
          '/api/v1/sessions/' + segment(machine.id) + '/' + segment(session.id) + '/messages',
          'POST',
          Wire.payload(['text', 'client_msg_id'], [cleanPrompt, opId]),
          opId
        );
      } catch (error) {
        if (error instanceof HeadlessHubFailure && error.code === 'xiaoyi_hub_update_required') {
          throw new HeadlessHubFailure('xiaoyi_no_live_session', conn.host, session.title || session.id);
        }
        throw error as Error;
      }
      return {
        machineId: machine.id,
        machineName: machine.name || machine.id,
        sessionId: session.id,
        sessionTitle: session.title || session.id,
        createdNew: false,
        deepLinkUri: sessionRouteUri(conn.profile.id, conn.profile.manager, machine.id, session.id)
      };
    } finally {
      conn.transport.close();
    }
  }

  /** Approve the oldest pending eligible permission/plan. Always fails closed when the screen is locked. */
  async approveFirstPending(overrideLocked?: boolean): Promise<HeadlessApproveResult> {
    const locked = overrideLocked !== undefined ? overrideLocked : await this.isDeviceLocked();
    if (locked) {
      throw new HeadlessHubFailure('xiaoyi_unlock_required');
    }
    const conn = await this.openConnection();
    try {
      const sessionsRes = await this.requestJson(conn, '/api/v1/sessions');
      const sessions = decodeSessions(Wire.rows(sessionsRes, 'sessions'));
      const interactionsRes = await this.requestJson(conn, '/api/v1/interactions');
      const interactions = decodeInteractions(Wire.rows(interactionsRes, 'interactions'));
      const mutes = await this.readMutes(conn);
      const pending = selectPendingApprovals(interactions, sessions, mutes);
      if (pending.length === 0) {
        throw new HeadlessHubFailure('xiaoyi_nothing_pending', conn.host);
      }
      const top = pending[0];
      const deepLinkUri = top.machineId && top.sessionId
        ? sessionRouteUri(conn.profile.id, conn.profile.manager, top.machineId, top.sessionId)
        : buildXiaoYiRouteUri(createXiaoYiRoute('agents'));
      const resolution = buildApprovalPayload(top);
      if (resolution.kind === 'needs_screen' || !resolution.body) {
        return {
          approved: false,
          needsScreen: true,
          decisionId: top.id,
          summary: top.summary || top.toolName || top.kind,
          reason: resolution.reason,
          deepLinkUri: deepLinkUri
        };
      }
      const opId = util.generateRandomUUID().toLowerCase();
      try {
        await this.requestJson(
          conn,
          '/api/v1/interactions/' + segment(top.id) + '/resolve',
          'POST',
          resolution.body,
          opId
        );
      } catch (error) {
        if (error instanceof HeadlessHubFailure && error.code === 'xiaoyi_hub_update_required') {
          throw new HeadlessHubFailure('xiaoyi_already_handled', conn.host);
        }
        throw error as Error;
      }
      return {
        approved: true,
        needsScreen: false,
        decisionId: top.id,
        summary: top.summary || top.toolName || top.kind,
        reason: resolution.reason,
        deepLinkUri: deepLinkUri
      };
    } finally {
      conn.transport.close();
    }
  }

  /** Resolve a specific decision ID from an inline AgentCard confirmation, still fail-closed on lock screen. */
  async resolveDecision(
    decisionId: string,
    body: Record<string, Object>,
    overrideLocked?: boolean,
    requestedProfileId: string = '',
    managerOverride?: string
  ): Promise<void> {
    if (!decisionId) throw new HeadlessHubFailure('xiaoyi_already_handled');
    const locked = overrideLocked !== undefined ? overrideLocked : await this.isDeviceLocked();
    if (locked) throw new HeadlessHubFailure('xiaoyi_unlock_required');
    const conn = await this.openConnection(requestedProfileId, managerOverride);
    try {
      const opId = util.generateRandomUUID().toLowerCase();
      await this.requestJson(conn, '/api/v1/interactions/' + segment(decisionId) + '/resolve', 'POST', body, opId);
    } catch (error) {
      if (error instanceof HeadlessHubFailure && error.code === 'xiaoyi_hub_update_required') {
        throw new HeadlessHubFailure('xiaoyi_already_handled', conn.host);
      }
      throw error as Error;
    } finally {
      conn.transport.close();
    }
  }

  async addTodo(title: string, notes: string = ''): Promise<HeadlessTodoResult> {
    const cleanTitle = title.trim();
    if (!cleanTitle) throw new HeadlessHubFailure('xiaoyi_nothing_to_send');
    const conn = await this.openConnection();
    try {
      const body = Wire.payload(['title'], [cleanTitle]);
      const cleanNotes = notes.trim();
      if (cleanNotes) body['notes'] = cleanNotes;
      const opId = util.generateRandomUUID().toLowerCase();
      const res = await this.requestJson(conn, '/api/v1/todos', 'POST', body, opId);
      const todoObj = Wire.object(res, 'todo');
      const id = Wire.text(todoObj, 'id', Wire.text(res, 'id'));
      const savedTitle = Wire.text(todoObj, 'title', cleanTitle);
      return {
        id: id,
        title: savedTitle,
        deepLinkUri: buildXiaoYiRouteUri(createXiaoYiRoute('todos'))
      };
    } finally {
      conn.transport.close();
    }
  }

  async logWater(amountMl?: number): Promise<HeadlessWaterResult> {
    if (amountMl !== undefined && intakeAmount('water', String(amountMl)) === undefined) {
      throw new HeadlessHubFailure('xiaoyi_failed');
    }
    // Health intake always runs on the default manager (`managerOverride: ''`).
    const conn = await this.openConnection('', '');
    try {
      const state = await this.requestJson(conn, intakeRoute + HEALTH_QUERY);
      if (Wire.has(state, 'items')) {
        const items = Wire.strings(state, 'items');
        if (items.length > 0 && !items.includes('water')) {
          throw new HeadlessHubFailure('xiaoyi_water_not_enabled', conn.host);
        }
      }
      const waterObj = Wire.object(state, 'water');
      const goalMl = Wire.number(waterObj, 'goal');
      const entryId = util.generateRandomUUID().toLowerCase();
      const body = Wire.payload(['id', 'kind', 'at'], [entryId, 'water', new Date().toISOString()]);
      if (amountMl !== undefined) body['amount'] = amountMl;
      const posted = await this.requestJson(conn, intakeRoute + HEALTH_QUERY, 'POST', body, entryId);
      const todayObj = Wire.object(posted, 'today');
      const entryObj = Wire.object(posted, 'entry');
      const recordedMl = Wire.number(entryObj, 'amount', amountMl ?? 0);
      const todayMl = Wire.number(todayObj, 'water');
      return {
        recordedMl: recordedMl,
        todayMl: todayMl,
        goalMl: goalMl,
        deepLinkUri: buildXiaoYiRouteUri(createXiaoYiRoute('health'))
      };
    } finally {
      conn.transport.close();
    }
  }

  private pushOutboxKey(scope: string): string {
    return 'xiaoyi:' + scope + ':push-outbox';
  }

  private async loadOutbox(scope: string): Promise<XiaoYiPushItem[]> {
    const raw = await this.local.get(this.pushOutboxKey(scope));
    if (!raw) return [];
    try {
      const parsed = JSON.parse(raw) as Object[];
      if (!Array.isArray(parsed)) return [];
      const items: XiaoYiPushItem[] = [];
      for (let i = 0; i < parsed.length; i++) {
        const row = Wire.record(parsed[i]);
        const key = Wire.text(row, 'key');
        if (!key) continue;
        const link = Wire.text(row, 'link');
        items.push({
          key: key,
          title: Wire.text(row, 'title'),
          body: Wire.text(row, 'body'),
          app: Wire.text(row, 'app'),
          link: link || undefined,
          time: Wire.text(row, 'time'),
          createdAtMs: Wire.number(row, 'createdAtMs', Date.now())
        });
      }
      return pruneOutboxItems(items);
    } catch (_) {
      return [];
    }
  }

  private async saveOutbox(scope: string, items: XiaoYiPushItem[]): Promise<void> {
    const key = this.pushOutboxKey(scope);
    if (items.length === 0) {
      await this.local.remove(key);
      return;
    }
    await this.local.put(key, JSON.stringify(items));
  }

  async pushSubscriptionItem(title: string, body: string, app: string = '', link: string = ''): Promise<HeadlessPushResult> {
    const item = buildPushItem(title, body, app, link, Date.now());
    if (!item) throw new HeadlessHubFailure('xiaoyi_nothing_to_send');
    const conn = await this.openConnection();
    try {
      const existing = await this.loadOutbox(conn.scope);
      const merged = enqueueOutboxItem(existing, item, Date.now());
      const batch = takeOutboxBatch(merged);
      const wireItems: Record<string, Object>[] = batch.map((entry: XiaoYiPushItem): Record<string, Object> => {
        const obj = Wire.payload(['key', 'title', 'body', 'app', 'time'], [entry.key, entry.title, entry.body, entry.app, entry.time]);
        if (entry.link) obj['link'] = entry.link;
        return obj;
      });
      try {
        const res = await this.requestJson(
          conn,
          '/api/v1/subscriptions/-/push',
          'POST',
          Wire.payload(['items'], [wireItems])
        );
        const sentKeys = new Set<string>(batch.map((entry: XiaoYiPushItem): string => entry.key));
        const remaining = merged.filter((entry: XiaoYiPushItem): boolean => !sentKeys.has(entry.key));
        await this.saveOutbox(conn.scope, remaining);
        const sub = Wire.object(res, 'subscription');
        const hasDest = Wire.bool(sub, 'to_inbox') || Wire.bool(sub, 'to_kb');
        return {
          status: 'sent',
          newCount: Wire.number(res, 'new', batch.length),
          hasDestination: hasDest,
          queuedCount: remaining.length
        };
      } catch (error) {
        if (isTransientPushError(error as Error)) {
          await this.saveOutbox(conn.scope, merged);
          return {
            status: 'queued',
            newCount: 0,
            hasDestination: true,
            queuedCount: merged.length
          };
        }
        throw mapHttpError(error as Error, conn.host);
      }
    } finally {
      conn.transport.close();
    }
  }

  async searchKnowledge(query: string): Promise<HeadlessKnowledgeHit[]> {
    const clean = query.trim();
    if (!clean) throw new HeadlessHubFailure('xiaoyi_nothing_to_send');
    const conn = await this.openConnection();
    try {
      const res = await this.requestJson(conn, '/api/v1/kb-search?q=' + segment(clean) + '&messages=0');
      return Wire.rows(res, 'hits').slice(0, 10).map((row: Record<string, Object>): HeadlessKnowledgeHit => {
        return {
          path: Wire.text(row, 'path'),
          line: Wire.text(row, 'line')
        };
      });
    } finally {
      conn.transport.close();
    }
  }

  private contextMapKey(scope: string): string {
    return 'xiaoyi:' + scope + ':a2a-contexts';
  }

  async getContextConversation(contextId: string): Promise<HeadlessContextBinding | undefined> {
    if (!contextId) return undefined;
    try {
      const profile = await this.loadActiveProfile();
      const scope = scopeKey({ groupId: profile.id, managerId: profile.manager });
      const raw = await this.local.get(this.contextMapKey(scope));
      if (!raw) return undefined;
      const map = Wire.parse(raw);
      const entry = Wire.object(map, contextId);
      const machineId = Wire.text(entry, 'machineId');
      const sessionId = Wire.text(entry, 'sessionId');
      if (!machineId || !sessionId) return undefined;
      return { machineId: machineId, sessionId: sessionId, updatedAt: Wire.number(entry, 'updatedAt') };
    } catch (_) {
      return undefined;
    }
  }

  async setContextConversation(contextId: string, machineId: string, sessionId: string): Promise<void> {
    if (!contextId || !machineId || !sessionId) return;
    try {
      const profile = await this.loadActiveProfile();
      const scope = scopeKey({ groupId: profile.id, managerId: profile.manager });
      const key = this.contextMapKey(scope);
      const raw = await this.local.get(key);
      const map = raw ? Wire.parse(raw) : Wire.payload([], []);
      map[contextId] = Wire.payload(['machineId', 'sessionId', 'updatedAt'], [machineId, sessionId, Date.now()]);
      const keys = Object.keys(map);
      let nextMap: Record<string, Object> = map;
      if (keys.length > MAX_A2A_CONTEXTS) {
        keys.sort((a: string, b: string): number =>
          Wire.number(Wire.object(map, a), 'updatedAt') - Wire.number(Wire.object(map, b), 'updatedAt')
        );
        const keep = keys.slice(keys.length - MAX_A2A_CONTEXTS);
        nextMap = Wire.payload([], []);
        for (let i = 0; i < keep.length; i++) {
          const k = keep[i];
          nextMap[k] = map[k];
        }
      }
      await this.local.put(key, JSON.stringify(nextMap));
    } catch (_) {
      // Non-fatal persistence failure.
    }
  }

  async runDiagnostics(): Promise<HeadlessDiagnosticReport> {
    const report: HeadlessDiagnosticReport = {
      paired: false,
      profileName: '',
      hubHost: '',
      credentialReadable: false,
      sshTunnelOnly: false,
      hubReachable: false,
      recordingAllowed: true,
      onlineMachines: 0,
      totalMachines: 0,
      errorCode: ''
    };
    try {
      const profile = await this.loadActiveProfile();
      report.paired = true;
      report.profileName = profile.name || profile.hubId || profile.id;
      report.hubHost = extractHostLabel(profile.baseUrl);
      report.sshTunnelOnly = (await this.sshRouteMode(profile.id)) === 'required';
      const token = await this.vault.get(profile.id);
      report.credentialReadable = !!token;
      report.recordingAllowed = await this.isRecordingModuleAllowed(profile.id);
      if (report.sshTunnelOnly) {
        report.errorCode = 'xiaoyi_ssh_open_app';
        return report;
      }
      if (!report.credentialReadable) {
        report.errorCode = 'xiaoyi_auth_expired';
        return report;
      }
      const machines = await this.listMachines();
      report.hubReachable = true;
      report.totalMachines = machines.length;
      report.onlineMachines = machines.filter((m: XiaoYiMachine): boolean => m.online && m.trusted).length;
      return report;
    } catch (error) {
      const mapped = mapHttpError(error as Error, report.hubHost);
      report.errorCode = mapped.code;
      return report;
    }
  }
}
