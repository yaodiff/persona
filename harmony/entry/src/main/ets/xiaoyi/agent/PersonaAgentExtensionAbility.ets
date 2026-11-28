import { AgentExtensionAbility, Want, common } from '@kit.AbilityKit';
import {
  Message,
  Part,
  RequestContext,
  Role,
  Server,
  TaskArtifactParam,
  TaskState,
  TaskStatus,
  createA2AServer
} from '@kit.AgentFrameworkKit';
import { util } from '@kit.ArkTS';
import {
  A2AArtifactUpdate,
  A2AAskResult,
  A2AContextBinding,
  A2AHubBridge,
  A2AKnowledgeHit,
  A2APart,
  A2ARequestSnapshot,
  A2ASendTaskResult,
  A2ATaskSink,
  A2ATaskState,
  A2ATaskStateValue,
  A2ATodoResult,
  A2AWaterResult,
  XiaoYiA2AEngine
} from '../../core/XiaoYiA2AEngine';
import { XiaoYiStatusSummary } from '../../core/XiaoYiActions';
import { Strings } from '../../design/Strings';
import { HeadlessHubService } from '../HeadlessHubService';
import { XiaoYiRuntimeContext } from '../XiaoYiRuntimeContext';

function toSdkTaskState(state: A2ATaskStateValue): TaskState {
  switch (state) {
    case A2ATaskState.SUBMITTED: return TaskState.SUBMITTED;
    case A2ATaskState.WORKING: return TaskState.WORKING;
    case A2ATaskState.INPUT_REQUIRED: return TaskState.INPUT_REQUIRED;
    case A2ATaskState.COMPLETED: return TaskState.COMPLETED;
    case A2ATaskState.CANCELED: return TaskState.CANCELED;
    case A2ATaskState.FAILED: return TaskState.FAILED;
    case A2ATaskState.REJECTED: return TaskState.REJECTED;
    case A2ATaskState.AUTH_REQUIRED: return TaskState.AUTH_REQUIRED;
    default: return TaskState.UNSPECIFIED;
  }
}

function fromSdkParts(parts: Part[] | undefined): A2APart[] {
  if (!parts || parts.length === 0) return [];
  const result: A2APart[] = [];
  for (let i = 0; i < parts.length; i++) {
    const p = parts[i];
    const item: A2APart = {};
    if (p.text !== undefined) item.text = p.text;
    if (p.url !== undefined) item.url = p.url;
    if (p.filename !== undefined) item.filename = p.filename;
    if (p.mediaType !== undefined) item.mediaType = p.mediaType;
    if (p.data !== undefined) item.data = p.data as Object;
    result.push(item);
  }
  return result;
}

function toSdkParts(parts: A2APart[]): Part[] {
  const result: Part[] = [];
  for (let i = 0; i < parts.length; i++) {
    const p = parts[i];
    const out: Part = {};
    if (p.text !== undefined) out.text = p.text;
    if (p.url !== undefined) out.url = p.url;
    if (p.filename !== undefined) out.filename = p.filename;
    if (p.mediaType !== undefined) out.mediaType = p.mediaType;
    if (p.data !== undefined) out.data = p.data as object;
    result.push(out);
  }
  return result;
}

class HeadlessA2ABridge implements A2AHubBridge {
  private readonly hub: HeadlessHubService;

  constructor(hub: HeadlessHubService) {
    this.hub = hub;
  }

  async askPersona(
    question: string,
    conversationId: string,
    machineId: string,
    onProgress: (text: string, done: boolean) => void,
    isCancelled: () => boolean,
    onTarget?: (machineId: string, sessionId: string) => void
  ): Promise<A2AAskResult> {
    const res = await this.hub.askPersona(question, {
      conversationId: conversationId,
      machineId: machineId,
      onProgress: onProgress,
      onTarget: onTarget,
      isCancelled: isCancelled
    });
    return {
      answer: res.answer,
      pending: res.pending,
      machineId: res.machineId,
      sessionId: res.sessionId,
      deepLinkUri: res.deepLinkUri,
      askUserPrompt: res.askUserPrompt
    };
  }

  checkStatus(): Promise<XiaoYiStatusSummary> {
    return this.hub.checkStatus();
  }

  isRecordingAllowed(): Promise<boolean> {
    return this.hub.isRecordingModuleAllowed();
  }

  async addTodo(title: string, notes: string): Promise<A2ATodoResult> {
    const saved = await this.hub.addTodo(title, notes);
    return { id: saved.id, title: saved.title, deepLinkUri: saved.deepLinkUri };
  }

  async logWater(amountMl?: number): Promise<A2AWaterResult> {
    const logged = await this.hub.logWater(amountMl);
    return {
      recordedMl: logged.recordedMl,
      todayMl: logged.todayMl,
      goalMl: logged.goalMl,
      deepLinkUri: logged.deepLinkUri
    };
  }

  async searchKnowledge(query: string): Promise<A2AKnowledgeHit[]> {
    const hits = await this.hub.searchKnowledge(query);
    return hits.map((h): A2AKnowledgeHit => ({ path: h.path, line: h.line }));
  }

  async sendAgentTask(prompt: string, machineName: string, sessionName: string): Promise<A2ASendTaskResult> {
    const sent = await this.hub.sendAgentTask(prompt, machineName, sessionName, false);
    return {
      machineId: sent.machineId,
      machineName: sent.machineName,
      sessionId: sent.sessionId,
      sessionTitle: sent.sessionTitle,
      deepLinkUri: sent.deepLinkUri
    };
  }

  interruptSession(machineId: string, sessionId: string): Promise<void> {
    return this.hub.interruptSession(machineId, sessionId);
  }

  async getContextConversation(contextId: string): Promise<A2AContextBinding | undefined> {
    const binding = await this.hub.getContextConversation(contextId);
    if (!binding) return undefined;
    return { machineId: binding.machineId, sessionId: binding.sessionId };
  }

  setContextConversation(contextId: string, machineId: string, sessionId: string): Promise<void> {
    return this.hub.setContextConversation(contextId, machineId, sessionId);
  }

  formatError(error: Error): string {
    return XiaoYiRuntimeContext.formatError(error);
  }
}

class ServerTaskSink implements A2ATaskSink {
  private readonly server: Server;

  constructor(server: Server) {
    this.server = server;
  }

  updateStatus(taskId: string, state: A2ATaskStateValue, messageText?: string): void {
    const status: TaskStatus = {
      state: toSdkTaskState(state),
      timestamp: new Date().toISOString()
    };
    if (messageText) {
      const msg: Message = {
        messageId: util.generateRandomUUID().toLowerCase(),
        role: Role.AGENT,
        parts: [{ text: messageText, mediaType: 'text/plain' }],
        taskId: taskId
      };
      status.message = msg;
    }
    try {
      this.server.updateStatus(taskId, status);
    } catch (_) {
      // Ignore post-disconnect status updates.
    }
  }

  addArtifact(taskId: string, artifact: A2AArtifactUpdate): void {
    const param: TaskArtifactParam = {
      parts: toSdkParts(artifact.parts),
      artifactId: artifact.artifactId,
      name: artifact.name,
      append: artifact.append,
      lastChunk: artifact.lastChunk
    };
    if (artifact.metadata) {
      param.metadata = artifact.metadata as object;
    }
    try {
      this.server.addArtifact(taskId, param);
    } catch (_) {
      // Ignore post-disconnect artifact writes.
    }
  }
}

/**
 * HarmonyOS 7 (API 26) On-Device A2A v1.0 Agent Extension Ability for XiaoYi.
 * Handles streaming dialogue, PerceptionSuggest chips, multimodal drag/share parts, and CancelTask.
 */
export default class PersonaAgentExtensionAbility extends AgentExtensionAbility {
  private hub: HeadlessHubService | undefined = undefined;
  private engine: XiaoYiA2AEngine | undefined = undefined;
  private server: Server | undefined = undefined;

  onCreate(_want: Want): void {
    Strings.bind(this.context.resourceManager);
    XiaoYiRuntimeContext.bind(this.context);
    this.hub = new HeadlessHubService(this.context);
    this.engine = new XiaoYiA2AEngine(new HeadlessA2ABridge(this.hub));
  }

  onConnect(want: Want, _proxy: common.AgentHostProxy): void {
    if (!this.hub || !this.engine) {
      this.onCreate(want);
    }
    if (this.server) {
      try { this.server.stop(); } catch (_) {}
      this.server = undefined;
    }
    const server = createA2AServer(
      this.context.agentCard,
      (method: string, reqCtx: RequestContext): void => {
        this.handleA2ARequest(method, reqCtx);
      },
      want
    );
    this.server = server;
    server.start();
  }

  private handleA2ARequest(method: string, reqCtx: RequestContext): void {
    if (!this.engine || !this.server || !this.hub) return;
    const taskId = reqCtx.getTaskId() || util.generateRandomUUID().toLowerCase();
    const contextId = reqCtx.getContextId() || '';
    const message = reqCtx.getMessage();
    const snapshot: A2ARequestSnapshot = {
      taskId: taskId,
      contextId: contextId,
      userInput: reqCtx.getUserInput() || '',
      parts: fromSdkParts(message?.parts),
      locale: this.hub.effectiveLocale()
    };
    const sink = new ServerTaskSink(this.server);
    this.engine.handleMethod(method, snapshot, sink).catch(() => {
      sink.updateStatus(taskId, A2ATaskState.FAILED);
    });
  }

  onAuth(proxy: common.AgentHostProxy, handshakeData: string): void {
    if (!this.server) return;
    try {
      const reply = this.server.onAuth(handshakeData);
      proxy.authorize(reply);
    } catch (_) {
      // Handshake failure will be surfaced by host runtime.
    }
  }

  onData(proxy: common.AgentHostProxy, data: string): void {
    if (!this.server) return;
    try {
      this.server.onMessage(data, (resp: string): void => {
        try {
          proxy.sendData(resp);
        } catch (_) {
          // Host disconnected mid-response.
        }
      });
    } catch (_) {
      // Ignore malformed IPC payload.
    }
  }

  onDisconnect(_want: Want, _proxy: common.AgentHostProxy): void {
    this.engine?.cancelAll();
    if (this.server) {
      try { this.server.stop(); } catch (_) {}
      this.server = undefined;
    }
  }

  onDestroy(): void {
    this.engine?.cancelAll();
    if (this.server) {
      try { this.server.stop(); } catch (_) {}
      this.server = undefined;
    }
    this.hub?.close().catch(() => {});
    this.hub = undefined;
    this.engine = undefined;
  }
}
