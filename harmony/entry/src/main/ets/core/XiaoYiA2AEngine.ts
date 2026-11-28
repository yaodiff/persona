/** Pure on-device A2A v1.0 orchestrator for XiaoYi AgentExtensionAbility. Zero platform imports. */

import type { XiaoYiInteraction, XiaoYiStatusSummary } from './XiaoYiActions';
import { buildApprovalPayload, recordingModuleRefusalText } from './XiaoYiActions';
import { buildXiaoYiRouteUri, createXiaoYiRoute } from './XiaoYiRoutes';

/** Numeric task states aligned 1:1 with `@hms.ai.A2A.TaskState`. */
export class A2ATaskState {
  static readonly SUBMITTED: number = 0;
  static readonly WORKING: number = 1;
  static readonly INPUT_REQUIRED: number = 2;
  static readonly COMPLETED: number = 3;
  static readonly CANCELED: number = 4;
  static readonly FAILED: number = 5;
  static readonly REJECTED: number = 6;
  static readonly AUTH_REQUIRED: number = 7;
  static readonly UNSPECIFIED: number = 8;
}

export type A2ATaskStateValue = number;

export interface A2APart {
  text?: string;
  url?: string;
  filename?: string;
  mediaType?: string;
  data?: Object;
}

export interface A2ARequestSnapshot {
  taskId: string;
  contextId: string;
  userInput: string;
  parts: A2APart[];
  locale: string;
}

export interface A2AArtifactUpdate {
  artifactId: string;
  name: string;
  parts: A2APart[];
  metadata?: Record<string, Object>;
  append: boolean;
  lastChunk: boolean;
}

export interface A2ATaskSink {
  updateStatus(taskId: string, state: A2ATaskStateValue, messageText?: string): void;
  addArtifact(taskId: string, artifact: A2AArtifactUpdate): void;
}

export interface A2AAskResult {
  answer: string;
  pending: boolean;
  machineId: string;
  sessionId: string;
  deepLinkUri: string;
  askUserPrompt: string;
}

export interface A2ATodoResult {
  id: string;
  title: string;
  deepLinkUri: string;
}

export interface A2AWaterResult {
  recordedMl: number;
  todayMl: number;
  goalMl: number;
  deepLinkUri: string;
}

export interface A2AKnowledgeHit {
  path: string;
  line: string;
}

export interface A2ASendTaskResult {
  machineId: string;
  machineName: string;
  sessionId: string;
  sessionTitle: string;
  deepLinkUri: string;
}

export interface A2AContextBinding {
  machineId: string;
  sessionId: string;
}

export interface A2AHubBridge {
  askPersona(
    question: string,
    conversationId: string,
    machineId: string,
    onProgress: (text: string, done: boolean) => void,
    isCancelled: () => boolean,
    onTarget?: (machineId: string, sessionId: string) => void
  ): Promise<A2AAskResult>;
  checkStatus(): Promise<XiaoYiStatusSummary>;
  isRecordingAllowed?(): Promise<boolean>;
  addTodo(title: string, notes: string): Promise<A2ATodoResult>;
  logWater(amountMl?: number): Promise<A2AWaterResult>;
  searchKnowledge(query: string): Promise<A2AKnowledgeHit[]>;
  sendAgentTask(prompt: string, machineName: string, sessionName: string): Promise<A2ASendTaskResult>;
  interruptSession(machineId: string, sessionId: string): Promise<void>;
  getContextConversation(contextId: string): Promise<A2AContextBinding | undefined>;
  setContextConversation(contextId: string, machineId: string, sessionId: string): Promise<void>;
  formatError(error: Error): string;
}

export type A2AClassifiedKind =
  | 'status'
  | 'inspect_approval'
  | 'start_recording'
  | 'add_todo'
  | 'log_water'
  | 'search_kb'
  | 'send_agent'
  | 'ask_persona';

export interface A2AClassifiedIntent {
  kind: A2AClassifiedKind;
  arg: string;
  amountMl?: number;
}

export interface A2ASuggestionChip {
  id: string;
  title: string;
  query: string;
  deepLinkUri: string;
}

const MAX_PROMPT_CHARS: number = 8000;
const MAX_CHUNK_CHARS: number = 4096;
const MAX_CONCURRENT_TASKS: number = 8;
const PERCEPTION_BUDGET_MS: number = 1800;

interface ActiveTaskHandle {
  cancelled: boolean;
  machineId: string;
  sessionId: string;
}

function clipChars(text: string, maxChars: number): string {
  const chars = Array.from(text);
  return chars.length <= maxChars ? text : chars.slice(0, maxChars).join('');
}

function normalizeLang(locale: string): 'zh-Hans' | 'ja' | 'en' {
  const l = (locale || '').toLowerCase();
  if (l.startsWith('zh')) return 'zh-Hans';
  if (l.startsWith('ja')) return 'ja';
  return 'en';
}

function isAuthError(error: Error): boolean {
  const msg = error.message || '';
  return msg === 'xiaoyi_not_paired' || msg === 'xiaoyi_auth_expired';
}

/** Combine text, URL, file, and structured data A2A parts into a bounded prompt. */
export function extractMultimodalPrompt(parts: readonly A2APart[], fallbackText: string = ''): string {
  const pieces: string[] = [];
  for (let i = 0; i < parts.length; i++) {
    const part = parts[i];
    if (part.text && part.text.trim()) {
      pieces.push(part.text.trim());
    }
    if (part.url && part.url.trim()) {
      const url = part.url.trim();
      if (/^https?:\/\//i.test(url) || /^file:\/\//i.test(url)) {
        const label = part.filename ? `[${part.filename}] ` : '';
        pieces.push(label + url);
      }
    } else if (part.filename && part.filename.trim()) {
      const media = part.mediaType ? ` (${part.mediaType})` : '';
      pieces.push(`[File: ${part.filename.trim()}${media}]`);
    }
    if (part.data !== undefined && part.data !== null) {
      try {
        const serialized = typeof part.data === 'string' ? part.data : JSON.stringify(part.data);
        if (serialized && serialized !== '{}' && serialized !== '[]') {
          pieces.push(clipChars(serialized, 1500));
        }
      } catch (_) {
        // Ignore unserializable data parts.
      }
    }
  }
  if (pieces.length === 0 && fallbackText && fallbackText.trim()) {
    pieces.push(fallbackText.trim());
  }
  return clipChars(pieces.join('\n').trim(), MAX_PROMPT_CHARS);
}

/** Route structured quick commands vs open-ended assistant dialogue. */
export function classifyA2AIntent(prompt: string): A2AClassifiedIntent {
  const clean = prompt.trim();
  const lowered = clean.toLowerCase();

  // Add to-do
  const todoMatch = /^(?:add\s+(?:a\s+)?to-?do|new\s+to-?do|新建待办|记个待办|加个待办|添加待办|待办|ToDoを追加|タスクを追加)\s*[:：]?\s*(.+)$/i.exec(clean);
  if (todoMatch && todoMatch[1].trim()) {
    return { kind: 'add_todo', arg: todoMatch[1].trim() };
  }

  // Log water
  if (/^(?:log\s+water|drink\s+water|记喝水|喝了一杯水|喝水|记录喝水|水分を記録)/i.test(clean)) {
    const mlMatch = /(\d{1,4})\s*(?:ml|毫升|ミリリットル)?/i.exec(clean);
    const parsedMl = mlMatch ? Number(mlMatch[1]) : undefined;
    const validMl = parsedMl !== undefined && parsedMl >= 1 && parsedMl <= 3000 ? parsedMl : undefined;
    return { kind: 'log_water', arg: '', amountMl: validMl };
  }

  // Knowledge search
  const kbMatch = /^(?:search\s+(?:persona\s+)?knowledge|find\s+note|搜索知识库|查知识库|在知识库(?:里|中)?(?:搜索|查)|ナレッジ検索)\s*[:：]?\s*(.+)$/i.exec(clean);
  if (kbMatch && kbMatch[1].trim()) {
    return { kind: 'search_kb', arg: kbMatch[1].trim() };
  }

  // Dispatch to Agent
  const agentMatch = /^(?:send\s+to\s+agent|ask\s+claude|have\s+(?:an\s+)?agent|让\s*agent\s*(?:去做)?|交给\s*agent|エージェントに依頼)\s*[:：]?\s*(.+)$/i.exec(clean);
  if (agentMatch && agentMatch[1].trim()) {
    return { kind: 'send_agent', arg: agentMatch[1].trim() };
  }

  // Start meeting recording (strictly gated by the Hub's `module.recording` flag)
  if (/^(?:start\s+(?:a\s+)?(?:meeting\s+)?recording|record\s+(?:a\s+)?meeting|开始(?:会议)?录音|用\s*persona\s*录音|会議を録音|録音を開始)/i.test(clean)) {
    return { kind: 'start_recording', arg: '' };
  }

  // Inspect pending approvals (always prompts inline confirmation card or Persona App, never silent auto-approve)
  if (/^(?:approve|allow\s+first|review\s+pending|批准|通过待确认|查看待审批|审批|確認待ちを承認)/i.test(clean) ||
    lowered === 'pending approvals') {
    return { kind: 'inspect_approval', arg: '' };
  }

  // Status check
  if (/^(?:persona\s+status|check\s+status|status|how(?:'s| is)\s+persona|查看状态|检查状态|任务进度|工作状态|待确认|状況確認|ステータス)/i.test(clean)) {
    return { kind: 'status', arg: '' };
  }

  return { kind: 'ask_persona', arg: clean };
}

function localizedTodoReceipt(title: string, locale: string): string {
  const lang = normalizeLang(locale);
  if (lang === 'zh-Hans') return `已保存待办：${title}`;
  if (lang === 'ja') return `ToDoを保存しました: ${title}`;
  return `To-do saved: ${title}`;
}

function localizedWaterReceipt(recordedMl: number, todayMl: number, goalMl: number, locale: string): string {
  const lang = normalizeLang(locale);
  if (lang === 'zh-Hans') return `已记录 ${recordedMl} 毫升。今日：${todayMl} / ${goalMl} 毫升。`;
  if (lang === 'ja') return `${recordedMl} ml を記録しました。本日: ${todayMl} / ${goalMl} ml。`;
  return `Logged ${recordedMl} ml. Today: ${todayMl} / ${goalMl} ml.`;
}

function localizedKbEmpty(query: string, locale: string): string {
  const lang = normalizeLang(locale);
  if (lang === 'zh-Hans') return `知识库中未找到与“${query}”相关的内容。`;
  if (lang === 'ja') return `「${query}」に一致するナレッジは見つかりませんでした。`;
  return `No knowledge matches for "${query}".`;
}

function localizedAgentReceipt(sessionTitle: string, machineName: string, locale: string): string {
  const lang = normalizeLang(locale);
  if (lang === 'zh-Hans') return `已发送至 ${sessionTitle}（${machineName}）。`;
  if (lang === 'ja') return `${sessionTitle}（${machineName}）へ送信しました。`;
  return `Sent to ${sessionTitle} (${machineName}).`;
}

function localizedOpeningRecording(locale: string): string {
  const lang = normalizeLang(locale);
  if (lang === 'zh-Hans') return '正在打开 Persona 会议录音。';
  if (lang === 'ja') return 'Persona の会議録音を開きます。';
  return 'Opening Persona Meeting Recording.';
}

function localizedStillThinking(locale: string): string {
  const lang = normalizeLang(locale);
  if (lang === 'zh-Hans') return '已发送给 Persona，正在处理中——打开 Persona 可查看进度。';
  if (lang === 'ja') return 'Persona に送信しました。処理中です—進行状況は Persona で確認できます。';
  return 'Sent to Persona. It is still thinking — open Persona to view progress.';
}

function splitTextChunks(text: string, maxChars: number): string[] {
  const chars = Array.from(text);
  if (chars.length <= maxChars) return [text];
  const chunks: string[] = [];
  for (let offset = 0; offset < chars.length; offset += maxChars) {
    chunks.push(chars.slice(offset, offset + maxChars).join(''));
  }
  return chunks;
}

function buildSuggestionsForStatus(
  status: XiaoYiStatusSummary | undefined,
  authError: boolean,
  locale: string,
  recordingAllowed: boolean = true
): A2ASuggestionChip[] {
  const lang = normalizeLang(locale);
  const chips: A2ASuggestionChip[] = [];

  if (authError) {
    chips.push({
      id: 'pair_hub',
      title: lang === 'zh-Hans' ? '配对 Persona Hub' : lang === 'ja' ? 'Persona Hubをペアリング' : 'Pair Persona Hub',
      query: lang === 'zh-Hans' ? '打开 Persona 配对' : 'Open Persona to pair',
      deepLinkUri: buildXiaoYiRouteUri(createXiaoYiRoute('assistant'))
    });
    return chips;
  }

  if (status && status.pendingCount > 0) {
    const count = Math.min(status.pendingCount, 99);
    chips.push({
      id: 'review_pending',
      title: lang === 'zh-Hans' ? `处理 ${count} 项待确认` : lang === 'ja' ? `${count} 件の確認待ちを見る` : `Review ${count} pending requests`,
      query: lang === 'zh-Hans' ? '查看待确认' : lang === 'ja' ? '状況確認' : 'Check status',
      deepLinkUri: buildXiaoYiRouteUri(createXiaoYiRoute('agents'))
    });
  }

  if (status && status.workingCount > 0) {
    const count = Math.min(status.workingCount, 99);
    chips.push({
      id: 'check_working',
      title: lang === 'zh-Hans' ? `查看 ${count} 个进行中任务` : lang === 'ja' ? `実行中の ${count} 件を確認` : `Check ${count} running Agents`,
      query: lang === 'zh-Hans' ? '查看状态' : lang === 'ja' ? 'ステータス' : 'Persona status',
      deepLinkUri: buildXiaoYiRouteUri(createXiaoYiRoute('agents'))
    });
  }

  const defaults: A2ASuggestionChip[] = [
    {
      id: 'add_todo',
      title: lang === 'zh-Hans' ? '新建待办' : lang === 'ja' ? 'ToDoを追加' : 'Add a to-do',
      query: lang === 'zh-Hans' ? '新建待办：' : lang === 'ja' ? 'ToDoを追加: ' : 'Add a to-do: ',
      deepLinkUri: buildXiaoYiRouteUri(createXiaoYiRoute('todos'))
    },
    {
      id: 'ask_persona',
      title: lang === 'zh-Hans' ? '问一下 Persona' : lang === 'ja' ? 'Personaに聞く' : 'Ask Persona',
      query: lang === 'zh-Hans' ? '帮我整理今天的重点事项' : lang === 'ja' ? '今日の優先事項をまとめて' : 'Summarize my priorities today',
      deepLinkUri: buildXiaoYiRouteUri(createXiaoYiRoute('assistant'))
    },
    {
      id: 'log_water',
      title: lang === 'zh-Hans' ? '记一杯水' : lang === 'ja' ? '水分を記録' : 'Log water',
      query: lang === 'zh-Hans' ? '喝了一杯水' : lang === 'ja' ? '水分を記録' : 'Log water',
      deepLinkUri: buildXiaoYiRouteUri(createXiaoYiRoute('health'))
    }
  ];
  if (recordingAllowed) {
    const recRoute = createXiaoYiRoute('recording');
    recRoute.autoStart = true;
    defaults.push({
      id: 'start_recording',
      title: lang === 'zh-Hans' ? '开始会议录音' : lang === 'ja' ? '会議を録音' : 'Start meeting recording',
      query: lang === 'zh-Hans' ? '开始会议录音' : lang === 'ja' ? '会議を録音' : 'Start meeting recording',
      deepLinkUri: buildXiaoYiRouteUri(recRoute)
    });
  }

  for (let i = 0; i < defaults.length && chips.length < 4; i++) {
    chips.push(defaults[i]);
  }
  return chips.slice(0, 4);
}

function buildCardMetadata(cardType: string, deepLinkUri: string, extras?: Record<string, Object>): Record<string, Object> {
  const meta: Record<string, Object> = {};
  meta['cardType'] = cardType;
  meta['deepLinkUri'] = deepLinkUri;
  if (extras) {
    const keys = Object.keys(extras);
    for (let i = 0; i < keys.length; i++) {
      const k = keys[i];
      meta[k] = extras[k];
    }
  }
  return meta;
}

export class XiaoYiA2AEngine {
  private readonly bridge: A2AHubBridge;
  private readonly activeTasks: Map<string, ActiveTaskHandle> = new Map();
  private readonly perceptionBudgetMs: number;

  constructor(bridge: A2AHubBridge, perceptionBudgetMs: number = PERCEPTION_BUDGET_MS) {
    this.bridge = bridge;
    this.perceptionBudgetMs = perceptionBudgetMs;
  }

  activeTaskCount(): number {
    return this.activeTasks.size;
  }

  cancelAll(): void {
    const ids = Array.from(this.activeTasks.keys());
    for (let i = 0; i < ids.length; i++) {
      const handle = this.activeTasks.get(ids[i]);
      if (handle) {
        handle.cancelled = true;
        if (handle.machineId && handle.sessionId) {
          this.bridge.interruptSession(handle.machineId, handle.sessionId).catch(() => {});
        }
      }
    }
    this.activeTasks.clear();
  }

  async handleMethod(method: string, req: A2ARequestSnapshot, sink: A2ATaskSink): Promise<void> {
    if (method === 'Cancel') {
      await this.handleCancel(req.taskId, sink);
      return;
    }
    if (method === 'PerceptionSuggest') {
      await this.handlePerceptionSuggest(req, sink);
      return;
    }
    if (method === 'Execute') {
      await this.handleExecute(req, sink);
      return;
    }
    sink.updateStatus(req.taskId, A2ATaskState.REJECTED, 'Unsupported A2A method: ' + method);
  }

  private async handleCancel(taskId: string, sink: A2ATaskSink): Promise<void> {
    const handle = this.activeTasks.get(taskId);
    if (handle) {
      handle.cancelled = true;
      this.activeTasks.delete(taskId);
      if (handle.machineId && handle.sessionId) {
        try {
          await this.bridge.interruptSession(handle.machineId, handle.sessionId);
        } catch (_) {
          // Best-effort cancellation.
        }
      }
    }
    sink.updateStatus(taskId, A2ATaskState.CANCELED, 'Canceled');
  }

  private async handlePerceptionSuggest(req: A2ARequestSnapshot, sink: A2ATaskSink): Promise<void> {
    sink.updateStatus(req.taskId, A2ATaskState.WORKING);
    let status: XiaoYiStatusSummary | undefined = undefined;
    let recordingAllowed: boolean = true;
    let authError = false;

    let timerId: number = -1;
    try {
      const timeoutPromise = new Promise<void>((resolve) => {
        timerId = setTimeout(() => resolve(), this.perceptionBudgetMs);
      });
      const statusWork = this.bridge.checkStatus().then((res: XiaoYiStatusSummary) => {
        status = res;
      }).catch((error: Error) => {
        if (isAuthError(error)) authError = true;
      });
      const recWork = this.bridge.isRecordingAllowed
        ? this.bridge.isRecordingAllowed().then((allowed: boolean) => {
            recordingAllowed = allowed;
          }).catch(() => {
            // Keep default when allowance cannot be read.
          })
        : Promise.resolve();
      await Promise.race<void>([
        Promise.all([statusWork, recWork]).then((): void => undefined),
        timeoutPromise
      ]);
    } finally {
      if (timerId !== -1) clearTimeout(timerId);
    }

    const chips = buildSuggestionsForStatus(status, authError, req.locale, recordingAllowed);
    const payload: Record<string, Object> = {};
    payload['suggestions'] = chips as Object;
    sink.addArtifact(req.taskId, {
      artifactId: req.taskId + ':suggest',
      name: 'perception_suggestions',
      parts: [{ data: payload, mediaType: 'application/json' }],
      metadata: buildCardMetadata('status', buildXiaoYiRouteUri(createXiaoYiRoute('agents'))),
      append: false,
      lastChunk: true
    });
    sink.updateStatus(req.taskId, A2ATaskState.COMPLETED);
  }

  private async handleExecute(req: A2ARequestSnapshot, sink: A2ATaskSink): Promise<void> {
    const prompt = extractMultimodalPrompt(req.parts, req.userInput);
    if (!prompt) {
      sink.updateStatus(req.taskId, A2ATaskState.REJECTED, 'Empty prompt');
      return;
    }
    if (this.activeTasks.has(req.taskId)) {
      sink.updateStatus(req.taskId, A2ATaskState.REJECTED, 'Task already in progress');
      return;
    }
    if (this.activeTasks.size >= MAX_CONCURRENT_TASKS) {
      sink.updateStatus(req.taskId, A2ATaskState.REJECTED, 'Too many concurrent tasks');
      return;
    }

    const handle: ActiveTaskHandle = { cancelled: false, machineId: '', sessionId: '' };
    this.activeTasks.set(req.taskId, handle);

    // Super Rule 5: emit immediate SUBMITTED -> WORKING feedback before Hub I/O.
    sink.updateStatus(req.taskId, A2ATaskState.SUBMITTED);
    sink.updateStatus(req.taskId, A2ATaskState.WORKING);

    try {
      const intent = classifyA2AIntent(prompt);

      if (intent.kind === 'start_recording') {
        const allowed = this.bridge.isRecordingAllowed ? await this.bridge.isRecordingAllowed() : true;
        if (handle.cancelled) return;
        if (!allowed) {
          const refusal = recordingModuleRefusalText(req.locale);
          const readOnlyUri = buildXiaoYiRouteUri(createXiaoYiRoute('recording'));
          sink.addArtifact(req.taskId, {
            artifactId: req.taskId + ':recording_refused',
            name: 'persona_recording_refused',
            parts: [{ text: refusal, mediaType: 'text/plain' }],
            metadata: buildCardMetadata('receipt', readOnlyUri),
            append: false,
            lastChunk: true
          });
          sink.updateStatus(req.taskId, A2ATaskState.REJECTED, refusal);
          return;
        }
        const route = createXiaoYiRoute('recording');
        route.autoStart = true;
        const text = localizedOpeningRecording(req.locale);
        sink.addArtifact(req.taskId, {
          artifactId: req.taskId + ':recording',
          name: 'persona_recording_open',
          parts: [{ text: text, mediaType: 'text/plain' }],
          metadata: buildCardMetadata('receipt', buildXiaoYiRouteUri(route)),
          append: false,
          lastChunk: true
        });
        sink.updateStatus(req.taskId, A2ATaskState.COMPLETED, text);
        return;
      }

      if (intent.kind === 'status') {
        const summary = await this.bridge.checkStatus();
        if (handle.cancelled) return;
        const extras: Record<string, Object> = {};
        extras['workingCount'] = summary.workingCount;
        extras['pendingCount'] = summary.pendingCount;
        extras['unreadCount'] = summary.unreadPersonalCount;
        const uri = buildXiaoYiRouteUri(createXiaoYiRoute('agents'));
        sink.addArtifact(req.taskId, {
          artifactId: req.taskId + ':status',
          name: 'persona_status',
          parts: [{ text: summary.spokenText, mediaType: 'text/plain' }],
          metadata: buildCardMetadata('status', uri, extras),
          append: false,
          lastChunk: true
        });
        sink.updateStatus(req.taskId, summary.pendingCount > 0 ? A2ATaskState.INPUT_REQUIRED : A2ATaskState.COMPLETED, summary.headline);
        return;
      }

      if (intent.kind === 'inspect_approval') {
        const summary = await this.bridge.checkStatus();
        if (handle.cancelled) return;
        const top: XiaoYiInteraction | undefined = summary.topPending;
        if (!top) {
          sink.addArtifact(req.taskId, {
            artifactId: req.taskId + ':approval',
            name: 'persona_approval',
            parts: [{ text: summary.headline, mediaType: 'text/plain' }],
            metadata: buildCardMetadata('status', buildXiaoYiRouteUri(createXiaoYiRoute('agents'))),
            append: false,
            lastChunk: true
          });
          sink.updateStatus(req.taskId, A2ATaskState.COMPLETED, summary.headline);
          return;
        }
        const approvalPlan = buildApprovalPayload(top);
        let deepLink: string;
        if (top.machineId && top.sessionId) {
          const route = createXiaoYiRoute('session');
          route.profile = summary.profileId || '';
          route.manager = summary.managerId || '';
          route.machine = top.machineId;
          route.session = top.sessionId;
          deepLink = buildXiaoYiRouteUri(route);
        } else {
          deepLink = buildXiaoYiRouteUri(createXiaoYiRoute('agents'));
        }
        const extras: Record<string, Object> = {};
        extras['decisionId'] = top.id;
        extras['decisionKind'] = top.kind;
        extras['summary'] = top.summary || top.toolName || top.kind;
        extras['canApproveInline'] = approvalPlan.kind === 'resolve';
        if (summary.profileId) extras['profileId'] = summary.profileId;
        if (summary.managerId) extras['managerId'] = summary.managerId;
        sink.addArtifact(req.taskId, {
          artifactId: req.taskId + ':approval',
          name: 'persona_approval_card',
          parts: [{ text: summary.detail || summary.headline, mediaType: 'text/plain' }],
          metadata: buildCardMetadata('approval', deepLink, extras),
          append: false,
          lastChunk: true
        });
        sink.updateStatus(req.taskId, A2ATaskState.INPUT_REQUIRED, summary.detail || summary.headline);
        return;
      }

      if (intent.kind === 'add_todo') {
        const saved = await this.bridge.addTodo(intent.arg, '');
        if (handle.cancelled) return;
        const text = localizedTodoReceipt(saved.title, req.locale);
        sink.addArtifact(req.taskId, {
          artifactId: req.taskId + ':todo',
          name: 'persona_todo_receipt',
          parts: [{ text: text, mediaType: 'text/plain' }],
          metadata: buildCardMetadata('receipt', saved.deepLinkUri),
          append: false,
          lastChunk: true
        });
        sink.updateStatus(req.taskId, A2ATaskState.COMPLETED, text);
        return;
      }

      if (intent.kind === 'log_water') {
        const logged = await this.bridge.logWater(intent.amountMl);
        if (handle.cancelled) return;
        const text = localizedWaterReceipt(logged.recordedMl, logged.todayMl, logged.goalMl, req.locale);
        sink.addArtifact(req.taskId, {
          artifactId: req.taskId + ':water',
          name: 'persona_water_receipt',
          parts: [{ text: text, mediaType: 'text/plain' }],
          metadata: buildCardMetadata('receipt', logged.deepLinkUri),
          append: false,
          lastChunk: true
        });
        sink.updateStatus(req.taskId, A2ATaskState.COMPLETED, text);
        return;
      }

      if (intent.kind === 'search_kb') {
        const hits = await this.bridge.searchKnowledge(intent.arg);
        if (handle.cancelled) return;
        const text = hits.length === 0
          ? localizedKbEmpty(intent.arg, req.locale)
          : hits.slice(0, 5).map((h: A2AKnowledgeHit): string => h.path + (h.line ? ': ' + h.line : '')).join('\n');
        sink.addArtifact(req.taskId, {
          artifactId: req.taskId + ':kb',
          name: 'persona_knowledge_hits',
          parts: [{ text: clipChars(text, MAX_CHUNK_CHARS), mediaType: 'text/plain' }],
          metadata: buildCardMetadata('answer', buildXiaoYiRouteUri(createXiaoYiRoute('knowledge'))),
          append: false,
          lastChunk: true
        });
        sink.updateStatus(req.taskId, A2ATaskState.COMPLETED);
        return;
      }

      if (intent.kind === 'send_agent') {
        const sent = await this.bridge.sendAgentTask(intent.arg, '', '');
        if (handle.cancelled) return;
        const text = localizedAgentReceipt(sent.sessionTitle, sent.machineName, req.locale);
        sink.addArtifact(req.taskId, {
          artifactId: req.taskId + ':agent',
          name: 'persona_agent_receipt',
          parts: [{ text: text, mediaType: 'text/plain' }],
          metadata: buildCardMetadata('receipt', sent.deepLinkUri),
          append: false,
          lastChunk: true
        });
        sink.updateStatus(req.taskId, A2ATaskState.COMPLETED, text);
        return;
      }

      // General Persona Assistant dialogue with contextId persistence & incremental streaming.
      const existingBinding = req.contextId ? await this.bridge.getContextConversation(req.contextId) : undefined;
      if (handle.cancelled) return;
      if (existingBinding) {
        handle.machineId = existingBinding.machineId;
        handle.sessionId = existingBinding.sessionId;
      }

      const artifactId = req.taskId + ':answer';
      let emittedLen = 0;
      let chunkCount = 0;

      const askResult = await this.bridge.askPersona(
        prompt,
        existingBinding ? existingBinding.sessionId : '',
        existingBinding ? existingBinding.machineId : '',
        (fullText: string, done: boolean) => {
          if (handle.cancelled) return;
          const delta = fullText.length >= emittedLen ? fullText.slice(emittedLen) : '';
          if (delta) {
            emittedLen = fullText.length;
            const slices = splitTextChunks(delta, MAX_CHUNK_CHARS);
            for (let i = 0; i < slices.length; i++) {
              const isLast = done && i === slices.length - 1;
              sink.addArtifact(req.taskId, {
                artifactId: artifactId,
                name: 'persona_answer',
                parts: [{ text: slices[i], mediaType: 'text/plain' }],
                append: chunkCount > 0,
                lastChunk: isLast
              });
              chunkCount++;
            }
          } else if (done && chunkCount > 0) {
            sink.addArtifact(req.taskId, {
              artifactId: artifactId,
              name: 'persona_answer',
              parts: [{ text: '', mediaType: 'text/plain' }],
              append: true,
              lastChunk: true
            });
          }
        },
        (): boolean => handle.cancelled,
        (targetMachineId: string, targetSessionId: string) => {
          handle.machineId = targetMachineId;
          handle.sessionId = targetSessionId;
        }
      );

      if (handle.cancelled) return;
      handle.machineId = askResult.machineId;
      handle.sessionId = askResult.sessionId;
      if (req.contextId && askResult.machineId && askResult.sessionId) {
        await this.bridge.setContextConversation(req.contextId, askResult.machineId, askResult.sessionId);
      }

      if (askResult.askUserPrompt) {
        const extras: Record<string, Object> = {};
        extras['prompt'] = askResult.askUserPrompt;
        sink.addArtifact(req.taskId, {
          artifactId: req.taskId + ':ask_user',
          name: 'persona_input_required',
          parts: [{ text: clipChars(askResult.askUserPrompt, MAX_CHUNK_CHARS), mediaType: 'text/plain' }],
          metadata: buildCardMetadata('answer', askResult.deepLinkUri, extras),
          append: false,
          lastChunk: true
        });
        sink.updateStatus(req.taskId, A2ATaskState.INPUT_REQUIRED, askResult.askUserPrompt);
        return;
      }

      if (chunkCount === 0) {
        const finalText = askResult.answer || localizedStillThinking(req.locale);
        const slices = splitTextChunks(finalText, MAX_CHUNK_CHARS);
        for (let i = 0; i < slices.length; i++) {
          sink.addArtifact(req.taskId, {
            artifactId: artifactId,
            name: 'persona_answer',
            parts: [{ text: slices[i], mediaType: 'text/plain' }],
            metadata: i === 0 ? buildCardMetadata('answer', askResult.deepLinkUri) : undefined,
            append: i > 0,
            lastChunk: i === slices.length - 1
          });
        }
      }
      sink.updateStatus(req.taskId, A2ATaskState.COMPLETED);
    } catch (error) {
      if (handle.cancelled) return;
      const err = error as Error;
      const message = this.bridge.formatError(err);
      if (isAuthError(err)) {
        sink.addArtifact(req.taskId, {
          artifactId: req.taskId + ':auth',
          name: 'persona_auth_required',
          parts: [{ text: message, mediaType: 'text/plain' }],
          metadata: buildCardMetadata('receipt', buildXiaoYiRouteUri(createXiaoYiRoute('assistant'))),
          append: false,
          lastChunk: true
        });
        sink.updateStatus(req.taskId, A2ATaskState.AUTH_REQUIRED, message);
      } else {
        sink.addArtifact(req.taskId, {
          artifactId: req.taskId + ':error',
          name: 'persona_error',
          parts: [{ text: message, mediaType: 'text/plain' }],
          metadata: buildCardMetadata('receipt', buildXiaoYiRouteUri(createXiaoYiRoute('assistant'))),
          append: false,
          lastChunk: true
        });
        sink.updateStatus(req.taskId, A2ATaskState.FAILED, message);
      }
    } finally {
      if (this.activeTasks.get(req.taskId) === handle) {
        this.activeTasks.delete(req.taskId);
      }
    }
  }
}
