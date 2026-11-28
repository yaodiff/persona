#!/usr/bin/env node
/**
 * Standalone, zero-dependency XiaoYi Cloud A2A v1.0 & Streamable HTTP MCP Server for Persona Hub.
 *
 * Exposes a hardened HTTPS/HTTP-ready server behind a reverse proxy for Huawei XiaoYi Cloud
 * Agent (A2A v1.0 JSON-RPC + SSE streaming) and Cloud MCP (Streamable HTTP JSON-RPC 2.0),
 * backed directly by `PersonaHubClient` and shared helpers from `./xiaoyi-openclaw-bridge.mjs`.
 *
 * Configuration is supplied strictly via environment variables or a local 0600 JSON file
 * (--config <path> or XIAOYI_CONFIG_FILE) — never commit tokens or credentials.
 *
 * Usage:
 *   node harmony/scripts/xiaoyi-cloud-server.mjs [--config /path/to/cloud-config.json]
 *   node harmony/scripts/xiaoyi-cloud-server.mjs --check [--config /path/to/cloud-config.json]
 */

import { createHash, randomUUID, timingSafeEqual } from 'node:crypto';
import { readFileSync } from 'node:fs';
import http from 'node:http';
import { pathToFileURL } from 'node:url';
import {
  DEFAULT_ASK_TIMEOUT_MS,
  PersonaHubClient,
  RECORDING_OFF_MESSAGE,
  classifyRelayCommand,
  extractMessageText,
  isHubRecordingAllowed,
  sanitizePrompt
} from './xiaoyi-openclaw-bridge.mjs';

export const DEFAULT_CLOUD_HOST = '127.0.0.1';
export const DEFAULT_CLOUD_PORT = 18790;
export const MAX_BODY_BYTES = 1024 * 1024;
export const MIN_CLOUD_TOKEN_LENGTH = 24;

const MAX_TASKS = 256;
const TASK_TTL_MS = 30 * 60 * 1000;
const MAX_SESSION_CONTEXTS = 64;
const APPROVAL_REFUSAL_MESSAGE =
  'approval_requires_phone: For your security, approvals cannot be confirmed over cloud. Open the Persona app on your unlocked phone to review and approve.';
const RECORDING_ON_CLOUD_MESSAGE =
  'Open Persona on your phone to start Meeting Recording (microphone capture stays on-device).';

const MCP_TOOLS = [
  {
    name: 'ask_persona',
    description: 'Ask your personal Persona assistant a question or run a natural-language command.',
    inputSchema: {
      type: 'object',
      properties: {
        question: { type: 'string', description: 'Question or instruction for Persona.' },
        prompt: { type: 'string', description: 'Alias for question.' }
      },
      additionalProperties: false
    }
  },
  {
    name: 'check_status',
    description: 'Check running agent sessions and pending confirmation count on Persona Hub.',
    inputSchema: {
      type: 'object',
      properties: {},
      additionalProperties: false
    }
  },
  {
    name: 'send_agent_task',
    description: 'Send a prompt to an active non-assistant agent session on a paired computer.',
    inputSchema: {
      type: 'object',
      properties: {
        prompt: { type: 'string', description: 'Task instruction to send to the agent.' },
        machine: { type: 'string', description: 'Optional paired computer name or machine ID.' },
        session: { type: 'string', description: 'Optional target session ID or title.' }
      },
      required: ['prompt'],
      additionalProperties: false
    }
  },
  {
    name: 'add_todo',
    description: 'Add a new to-do item to Persona.',
    inputSchema: {
      type: 'object',
      properties: {
        title: { type: 'string', description: 'To-do title text.' }
      },
      required: ['title'],
      additionalProperties: false
    }
  },
  {
    name: 'log_water',
    description: 'Log daily water intake in milliliters to Persona Health.',
    inputSchema: {
      type: 'object',
      properties: {
        amount_ml: {
          type: 'integer',
          minimum: 1,
          maximum: 3000,
          description: 'Optional water amount in ml (1-3000). Uses Health default if omitted.'
        }
      },
      additionalProperties: false
    }
  },
  {
    name: 'search_knowledge',
    description: 'Search your personal Persona knowledge base.',
    inputSchema: {
      type: 'object',
      properties: {
        query: { type: 'string', description: 'Search query keywords.' }
      },
      required: ['query'],
      additionalProperties: false
    }
  }
];

export function parseCloudCliArgs(argv = process.argv.slice(2)) {
  const opts = { check: false, help: false, configFile: '' };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === '--check') opts.check = true;
    else if (arg === '--help' || arg === '-h') opts.help = true;
    else if (arg === '--config' && i + 1 < argv.length) opts.configFile = argv[++i];
    else if (arg.startsWith('--config=')) opts.configFile = arg.slice('--config='.length);
  }
  return opts;
}

export function loadCloudServerConfig(env = process.env, configFileOverride = '') {
  let fileCfg = {};
  const configPath = configFileOverride || env.XIAOYI_CONFIG_FILE || '';
  if (configPath) {
    const raw = readFileSync(configPath, 'utf8');
    fileCfg = JSON.parse(raw);
  }

  const pickStr = (envKey, fileKey, fallback = '') => {
    const raw = env[envKey] !== undefined && env[envKey] !== '' ? env[envKey] : fileCfg[fileKey];
    return typeof raw === 'string' ? raw.trim() : fallback;
  };

  const cloudToken = pickStr('XIAOYI_CLOUD_TOKEN', 'cloudToken');
  const host = pickStr('XIAOYI_CLOUD_HOST', 'host', DEFAULT_CLOUD_HOST) || DEFAULT_CLOUD_HOST;
  const hubUrl = pickStr('PERSONA_HUB_URL', 'hubUrl').replace(/\/+$/, '');
  const hubToken = pickStr('PERSONA_HUB_TOKEN', 'hubToken');
  const manager = pickStr('PERSONA_MANAGER', 'manager');
  const publicBaseUrl = pickStr('XIAOYI_PUBLIC_BASE_URL', 'publicBaseUrl').replace(/\/+$/, '');

  const rawPort =
    env.XIAOYI_CLOUD_PORT !== undefined && env.XIAOYI_CLOUD_PORT !== ''
      ? env.XIAOYI_CLOUD_PORT
      : fileCfg.port !== undefined && fileCfg.port !== ''
        ? fileCfg.port
        : DEFAULT_CLOUD_PORT;
  const port = Number(rawPort);
  if (!Number.isInteger(port) || port < 0 || port > 65535) {
    throw new Error('XIAOYI_CLOUD_PORT must be an integer between 0 and 65535');
  }

  const missing = [];
  if (!cloudToken) missing.push('XIAOYI_CLOUD_TOKEN');
  if (!hubUrl) missing.push('PERSONA_HUB_URL');
  if (!hubToken) missing.push('PERSONA_HUB_TOKEN');
  if (missing.length > 0) {
    throw new Error(`Missing required configuration: ${missing.join(', ')}`);
  }

  if (cloudToken.length < MIN_CLOUD_TOKEN_LENGTH) {
    throw new Error(`XIAOYI_CLOUD_TOKEN must be at least ${MIN_CLOUD_TOKEN_LENGTH} characters`);
  }
  if (!/^https?:\/\//i.test(hubUrl)) {
    throw new Error('PERSONA_HUB_URL must use http:// or https://');
  }
  if (publicBaseUrl && !/^https?:\/\//i.test(publicBaseUrl)) {
    throw new Error('XIAOYI_PUBLIC_BASE_URL must use http:// or https://');
  }

  let askTimeoutMs = Number(env.XIAOYI_ASK_TIMEOUT_MS || fileCfg.askTimeoutMs || DEFAULT_ASK_TIMEOUT_MS);
  if (!Number.isFinite(askTimeoutMs) || askTimeoutMs < 1000) {
    askTimeoutMs = DEFAULT_ASK_TIMEOUT_MS;
  }

  return {
    cloudToken,
    host,
    port,
    publicBaseUrl,
    hubUrl,
    hubToken,
    manager,
    askTimeoutMs
  };
}

/**
 * Constant-time Bearer token verifier using SHA-256 digests and `crypto.timingSafeEqual`
 * so comparison stays fixed-time regardless of input length.
 */
export function verifyBearerToken(authHeader, expectedToken) {
  if (typeof authHeader !== 'string' || typeof expectedToken !== 'string' || !expectedToken) {
    return false;
  }
  const match = /^Bearer\s+(.+)$/i.exec(authHeader.trim());
  if (!match) return false;
  const provided = match[1].trim();
  if (!provided) return false;
  const providedDigest = createHash('sha256').update(provided, 'utf8').digest();
  const expectedDigest = createHash('sha256').update(expectedToken, 'utf8').digest();
  return timingSafeEqual(providedDigest, expectedDigest);
}

function formatHostForUrl(host) {
  const raw = String(host || DEFAULT_CLOUD_HOST).trim();
  if (raw.includes(':') && !raw.startsWith('[')) return `[${raw}]`;
  return raw;
}

export function buildAgentCard(cfg, listenOrigin = '') {
  const hostForUrl = formatHostForUrl(cfg?.host || DEFAULT_CLOUD_HOST);
  const port = cfg?.port !== undefined && cfg?.port !== null ? cfg.port : DEFAULT_CLOUD_PORT;
  const baseUrl = String(cfg?.publicBaseUrl || listenOrigin || `http://${hostForUrl}:${port}`).replace(/\/+$/, '');
  const a2aUrl = `${baseUrl}/a2a`;

  return {
    name: 'Persona',
    description:
      'Personal AI assistant for everyday questions, workflow status, to-do capture, health water logging, agent delegation, and knowledge search.',
    version: '1.0.0',
    protocolVersion: '1.0',
    url: a2aUrl,
    interfaces: [
      {
        url: a2aUrl,
        protocolBinding: 'JSONRPC',
        protocolVersion: '1.0'
      }
    ],
    capabilities: {
      streaming: true,
      pushNotifications: false,
      stateTransitionHistory: true
    },
    defaultInputModes: ['text/plain', 'application/json'],
    defaultOutputModes: ['text/plain', 'application/json'],
    securitySchemes: {
      bearer: {
        type: 'http',
        scheme: 'bearer'
      }
    },
    security: [{ bearer: [] }],
    skills: [
      {
        id: 'ask_persona',
        name: 'Ask Persona',
        description: 'Ask your personal Persona assistant questions with multi-turn context and streaming replies.',
        tags: ['assistant', 'qa', 'chat', 'personal'],
        examples: ['Summarize my notes for today', '帮我整理今天的工作重点']
      },
      {
        id: 'check_status',
        name: 'Check Status',
        description: 'Check running agent sessions and pending confirmation requests on Persona Hub.',
        tags: ['status', 'approvals', 'workflow'],
        examples: ['Check status', '查看状态']
      },
      {
        id: 'send_agent_task',
        name: 'Send Agent Task',
        description: 'Send a task instruction to an active agent session on one of your paired computers.',
        tags: ['agent', 'task', 'automation'],
        examples: ['Send to agent: on Studio: run unit tests', '交给 Agent：运行单元测试']
      },
      {
        id: 'add_todo',
        name: 'Add To-Do',
        description: 'Capture a new to-do item in Persona.',
        tags: ['todo', 'tasks', 'productivity'],
        examples: ['Add a to-do: prepare release notes', '新建待办：整理周报']
      },
      {
        id: 'log_water',
        name: 'Log Water',
        description: 'Log daily water intake in milliliters to Persona Health.',
        tags: ['health', 'water', 'wellness'],
        examples: ['Log water 350 ml', '记喝水 250 毫升']
      },
      {
        id: 'search_knowledge',
        name: 'Search Knowledge',
        description: 'Search your personal Persona knowledge base for notes and documentation.',
        tags: ['knowledge', 'search', 'notes'],
        examples: ['Search knowledge: release checklist', '搜索知识库：发布清单']
      }
    ]
  };
}

function writeJson(res, statusCode, payload, extraHeaders = {}) {
  if (res.writableEnded) return;
  const body = JSON.stringify(payload);
  res.writeHead(statusCode, {
    'Content-Type': 'application/json; charset=utf-8',
    'Cache-Control': 'no-store',
    ...extraHeaders
  });
  res.end(body);
}

function rememberSessionContext(sessionContexts, contextId, binding) {
  if (!contextId || !binding?.machineId || !binding?.sessionId) return;
  sessionContexts.delete(contextId);
  sessionContexts.set(contextId, binding);
  while (sessionContexts.size > MAX_SESSION_CONTEXTS) {
    const oldest = sessionContexts.keys().next().value;
    sessionContexts.delete(oldest);
  }
}

function pruneTasks(taskStore, now = Date.now()) {
  for (const [id, task] of taskStore.entries()) {
    if (now - task.updatedAt > TASK_TTL_MS) {
      taskStore.delete(id);
    }
  }
  while (taskStore.size > MAX_TASKS) {
    const oldestId = taskStore.keys().next().value;
    taskStore.delete(oldestId);
  }
}

function saveTask(taskStore, task) {
  task.updatedAt = Date.now();
  taskStore.delete(task.id);
  taskStore.set(task.id, task);
  pruneTasks(taskStore, task.updatedAt);
}

function serializeTask(task) {
  return {
    id: task.id,
    contextId: task.contextId,
    status: task.status,
    artifacts: task.artifacts
  };
}

async function readJsonBody(req, maxBytes = MAX_BODY_BYTES) {
  const rawContentLen = req.headers['content-length'];
  if (rawContentLen !== undefined) {
    const declared = Number(rawContentLen);
    if (Number.isFinite(declared) && declared > maxBytes) {
      req.resume();
      return { ok: false, status: 413, error: 'Payload Too Large' };
    }
  }

  return new Promise((resolve) => {
    const chunks = [];
    let totalBytes = 0;
    let settled = false;

    const finish = (outcome) => {
      if (settled) return;
      settled = true;
      resolve(outcome);
    };

    req.on('data', (chunk) => {
      if (settled) return;
      totalBytes += chunk.length;
      if (totalBytes > maxBytes) {
        req.resume();
        finish({ ok: false, status: 413, error: 'Payload Too Large' });
        return;
      }
      chunks.push(chunk);
    });

    req.on('end', () => {
      if (settled) return;
      const raw = Buffer.concat(chunks).toString('utf8');
      if (!raw.trim()) {
        finish({ ok: false, status: 400, parseError: true });
        return;
      }
      try {
        const parsed = JSON.parse(raw);
        finish({ ok: true, value: parsed });
      } catch (_) {
        finish({ ok: false, status: 400, parseError: true });
      }
    });

    req.on('error', () => {
      finish({ ok: false, status: 400, parseError: true });
    });
  });
}

async function executeCommand(cmd, hubClient, { contextId = '', sessionContexts, taskState = null, onChunk = null } = {}) {
  if (cmd.kind === 'refuse_approval') {
    return { state: 'input-required', text: APPROVAL_REFUSAL_MESSAGE };
  }
  if (cmd.kind === 'start_recording') {
    const allowed = await hubClient.isRecordingAllowed();
    if (!allowed) {
      return { state: 'rejected', text: RECORDING_OFF_MESSAGE };
    }
    return { state: 'input-required', text: RECORDING_ON_CLOUD_MESSAGE };
  }
  if (cmd.kind === 'status') {
    return { state: 'completed', text: await hubClient.checkStatus() };
  }
  if (cmd.kind === 'add_todo') {
    return { state: 'completed', text: await hubClient.addTodo(cmd.arg) };
  }
  if (cmd.kind === 'log_water') {
    return { state: 'completed', text: await hubClient.logWater(cmd.amountMl) };
  }
  if (cmd.kind === 'send_agent') {
    return { state: 'completed', text: await hubClient.sendAgentTask(cmd.arg, cmd.machineName || '') };
  }
  if (cmd.kind === 'search_kb') {
    return { state: 'completed', text: await hubClient.searchKnowledge(cmd.arg) };
  }

  const existing = contextId && sessionContexts ? sessionContexts.get(contextId) : undefined;
  const res = await hubClient.askPersona(cmd.arg, {
    conversationId: existing?.sessionId || '',
    machineId: existing?.machineId || '',
    isCancelled: () => Boolean(taskState?.cancelled),
    onTarget: (target) => {
      if (taskState) {
        taskState.machineId = target.machineId;
        taskState.sessionId = target.sessionId;
      }
    },
    onChunk: (delta, lastChunk) => {
      if (onChunk && !taskState?.cancelled) {
        onChunk(delta, lastChunk);
      }
    }
  });
  if (res.canceled || taskState?.cancelled) {
    return { state: 'canceled', text: 'Task canceled.', canceled: true };
  }
  if (contextId && sessionContexts && res.machineId && res.sessionId) {
    rememberSessionContext(sessionContexts, contextId, { machineId: res.machineId, sessionId: res.sessionId });
  }
  return { state: 'completed', text: res.text };
}

async function handleA2aRequest(frame, res, { hubClient, taskStore, sessionContexts }) {
  if (!frame || typeof frame !== 'object' || Array.isArray(frame) || typeof frame.method !== 'string') {
    writeJson(res, 400, {
      jsonrpc: '2.0',
      id: frame?.id ?? null,
      error: { code: -32600, message: 'Invalid Request' }
    });
    return;
  }

  const id = frame.id ?? null;
  const method = frame.method;
  const params = frame.params && typeof frame.params === 'object' ? frame.params : {};

  if (method === 'tasks/get' || method === 'GetTask') {
    pruneTasks(taskStore);
    const taskId = String(params.id || params.taskId || '');
    const task = taskId ? taskStore.get(taskId) : null;
    if (!task) {
      writeJson(res, 200, {
        jsonrpc: '2.0',
        id,
        error: { code: -32001, message: 'Task not found' }
      });
      return;
    }
    writeJson(res, 200, { jsonrpc: '2.0', id, result: serializeTask(task) });
    return;
  }

  if (method === 'tasks/cancel' || method === 'CancelTask') {
    pruneTasks(taskStore);
    const taskId = String(params.id || params.taskId || '');
    const task = taskId ? taskStore.get(taskId) : null;
    if (!task) {
      writeJson(res, 200, {
        jsonrpc: '2.0',
        id,
        error: { code: -32001, message: 'Task not found' }
      });
      return;
    }
    task.cancelled = true;
    if (task.machineId && task.sessionId) {
      await hubClient.interrupt(task.machineId, task.sessionId);
    }
    task.status = {
      state: 'canceled',
      timestamp: new Date().toISOString()
    };
    saveTask(taskStore, task);
    writeJson(res, 200, {
      jsonrpc: '2.0',
      id,
      result: {
        id: task.id,
        contextId: task.contextId,
        status: task.status
      }
    });
    return;
  }

  const isSend = method === 'message/send' || method === 'SendMessage' || method === 'tasks/send';
  const isStream = method === 'message/stream' || method === 'SendStreamingMessage' || method === 'tasks/sendSubscribe';
  if (!isSend && !isStream) {
    writeJson(res, 200, {
      jsonrpc: '2.0',
      id,
      error: { code: -32601, message: `Unsupported method: ${method}` }
    });
    return;
  }

  const text = extractMessageText(params);
  if (!text) {
    writeJson(res, 200, {
      jsonrpc: '2.0',
      id,
      error: { code: -32602, message: 'Empty message content' }
    });
    return;
  }

  const taskId = String(params.id || params.taskId || params.message?.taskId || randomUUID());
  const contextId = String(params.contextId || params.sessionId || params.message?.contextId || randomUUID());
  const nowIso = new Date().toISOString();

  const task = {
    id: taskId,
    contextId,
    status: { state: 'working', timestamp: nowIso },
    artifacts: [],
    createdAt: Date.now(),
    updatedAt: Date.now(),
    cancelled: false,
    machineId: '',
    sessionId: ''
  };
  saveTask(taskStore, task);

  const cmd = classifyRelayCommand(text);

  if (!isStream) {
    try {
      const outcome = await executeCommand(cmd, hubClient, {
        contextId,
        sessionContexts,
        taskState: task
      });
      const ts = new Date().toISOString();
      if (task.cancelled || outcome.canceled) {
        task.status = { state: 'canceled', timestamp: ts };
        saveTask(taskStore, task);
        writeJson(res, 200, { jsonrpc: '2.0', id, result: serializeTask(task) });
        return;
      }
      task.status = {
        state: outcome.state,
        message: { role: 'agent', parts: [{ kind: 'text', text: outcome.text }] },
        timestamp: ts
      };
      task.artifacts = [
        {
          artifactId: `${taskId}:result`,
          parts: [{ kind: 'text', text: outcome.text }]
        }
      ];
      saveTask(taskStore, task);
      writeJson(res, 200, { jsonrpc: '2.0', id, result: serializeTask(task) });
    } catch (err) {
      const msg = err?.message || 'Hub request failed';
      task.status = {
        state: 'failed',
        message: { role: 'agent', parts: [{ kind: 'text', text: msg }] },
        timestamp: new Date().toISOString()
      };
      saveTask(taskStore, task);
      writeJson(res, 200, {
        jsonrpc: '2.0',
        id,
        error: { code: -32000, message: msg }
      });
    }
    return;
  }

  res.writeHead(200, {
    'Content-Type': 'text/event-stream; charset=utf-8',
    'Cache-Control': 'no-cache',
    Connection: 'keep-alive'
  });

  const sendSse = (payload) => {
    if (!res.writableEnded) {
      res.write(`data: ${JSON.stringify(payload)}\n\n`);
    }
  };

  let clientClosed = false;
  res.on('close', () => {
    if (!res.writableFinished) {
      clientClosed = true;
      task.cancelled = true;
    }
  });

  sendSse({
    jsonrpc: '2.0',
    id,
    result: {
      kind: 'status-update',
      taskId,
      contextId,
      final: false,
      status: {
        state: 'working',
        timestamp: new Date().toISOString()
      }
    }
  });

  try {
    let chunkIndex = 0;
    const outcome = await executeCommand(cmd, hubClient, {
      contextId,
      sessionContexts,
      taskState: task,
      onChunk: (delta, lastChunk) => {
        if (clientClosed || task.cancelled) return;
        if (!delta && !lastChunk) return;
        if (!delta && lastChunk && chunkIndex === 0) return;
        sendSse({
          jsonrpc: '2.0',
          id,
          result: {
            kind: 'artifact-update',
            taskId,
            contextId,
            append: chunkIndex > 0,
            lastChunk: Boolean(lastChunk),
            final: false,
            artifact: {
              artifactId: `${taskId}:answer`,
              parts: [{ kind: 'text', text: delta }]
            }
          }
        });
        chunkIndex++;
      }
    });

    if (clientClosed) {
      res.end();
      return;
    }

    const doneTs = new Date().toISOString();
    if (task.cancelled || outcome.canceled) {
      task.status = { state: 'canceled', timestamp: doneTs };
      saveTask(taskStore, task);
      sendSse({
        jsonrpc: '2.0',
        id,
        result: {
          kind: 'status-update',
          taskId,
          contextId,
          final: true,
          status: task.status
        }
      });
      res.end();
      return;
    }

    const finalArtifact = {
      artifactId: `${taskId}:result`,
      parts: [{ kind: 'text', text: outcome.text }]
    };
    task.status = {
      state: outcome.state,
      message: { role: 'agent', parts: [{ kind: 'text', text: outcome.text }] },
      timestamp: doneTs
    };
    task.artifacts = [finalArtifact];
    saveTask(taskStore, task);

    sendSse({
      jsonrpc: '2.0',
      id,
      result: {
        kind: 'artifact-update',
        taskId,
        contextId,
        append: false,
        lastChunk: true,
        final: true,
        artifact: finalArtifact
      }
    });
    sendSse({
      jsonrpc: '2.0',
      id,
      result: {
        kind: 'status-update',
        taskId,
        contextId,
        final: true,
        status: task.status
      }
    });
    res.end();
  } catch (err) {
    const msg = err?.message || 'Hub request failed';
    task.status = {
      state: 'failed',
      message: { role: 'agent', parts: [{ kind: 'text', text: msg }] },
      timestamp: new Date().toISOString()
    };
    saveTask(taskStore, task);
    sendSse({
      jsonrpc: '2.0',
      id,
      error: { code: -32000, message: msg }
    });
    res.end();
  }
}

function mcpTextResult(id, text, isError = false) {
  return {
    jsonrpc: '2.0',
    id,
    result: {
      content: [{ type: 'text', text: String(text) }],
      isError: Boolean(isError)
    }
  };
}

async function handleMcpRequest(frame, res, { hubClient }) {
  if (!frame || typeof frame !== 'object' || Array.isArray(frame) || typeof frame.method !== 'string') {
    writeJson(res, 400, {
      jsonrpc: '2.0',
      id: frame?.id ?? null,
      error: { code: -32600, message: 'Invalid Request' }
    });
    return;
  }

  const id = frame.id ?? null;
  const method = frame.method;
  const params = frame.params && typeof frame.params === 'object' ? frame.params : {};

  if (method === 'notifications/initialized' || (method.startsWith('notifications/') && frame.id === undefined)) {
    res.writeHead(202);
    res.end();
    return;
  }

  if (method === 'initialize') {
    writeJson(res, 200, {
      jsonrpc: '2.0',
      id,
      result: {
        protocolVersion: '2025-03-26',
        capabilities: { tools: { listChanged: false } },
        serverInfo: { name: 'persona-xiaoyi-cloud', version: '1.0.0' }
      }
    });
    return;
  }

  if (method === 'ping') {
    writeJson(res, 200, { jsonrpc: '2.0', id, result: {} });
    return;
  }

  if (method === 'tools/list') {
    writeJson(res, 200, { jsonrpc: '2.0', id, result: { tools: MCP_TOOLS } });
    return;
  }

  if (method !== 'tools/call') {
    writeJson(res, 200, {
      jsonrpc: '2.0',
      id,
      error: { code: -32601, message: `Unsupported method: ${method}` }
    });
    return;
  }

  const toolName = String(params.name || '');
  const args =
    params.arguments && typeof params.arguments === 'object' && !Array.isArray(params.arguments)
      ? params.arguments
      : {};

  try {
    if (toolName === 'ask_persona') {
      const question = sanitizePrompt(args.question ?? args.prompt ?? '');
      if (!question) {
        writeJson(res, 200, mcpTextResult(id, 'Missing required argument: question', true));
        return;
      }
      const cmd = classifyRelayCommand(question);
      const outcome = await executeCommand(cmd, hubClient, {});
      const isErr = outcome.state === 'rejected' || outcome.state === 'input-required';
      writeJson(res, 200, mcpTextResult(id, outcome.text, isErr));
      return;
    }

    if (toolName === 'check_status') {
      const text = await hubClient.checkStatus();
      writeJson(res, 200, mcpTextResult(id, text, false));
      return;
    }

    if (toolName === 'send_agent_task') {
      const prompt = sanitizePrompt(args.prompt ?? '');
      if (!prompt) {
        writeJson(res, 200, mcpTextResult(id, 'Missing required argument: prompt', true));
        return;
      }
      const machine = sanitizePrompt(args.machine ?? '');
      const session = sanitizePrompt(args.session ?? '');
      const text = await hubClient.sendAgentTask(prompt, machine, session);
      writeJson(res, 200, mcpTextResult(id, text, false));
      return;
    }

    if (toolName === 'add_todo') {
      const title = sanitizePrompt(args.title ?? '');
      if (!title) {
        writeJson(res, 200, mcpTextResult(id, 'Missing required argument: title', true));
        return;
      }
      const text = await hubClient.addTodo(title);
      writeJson(res, 200, mcpTextResult(id, text, false));
      return;
    }

    if (toolName === 'log_water') {
      let amountMl;
      if (args.amount_ml !== undefined && args.amount_ml !== null) {
        if (!Number.isInteger(args.amount_ml) || args.amount_ml < 1 || args.amount_ml > 3000) {
          writeJson(res, 200, mcpTextResult(id, 'Invalid amount_ml: must be an integer between 1 and 3000.', true));
          return;
        }
        amountMl = args.amount_ml;
      }
      const text = await hubClient.logWater(amountMl);
      writeJson(res, 200, mcpTextResult(id, text, false));
      return;
    }

    if (toolName === 'search_knowledge') {
      const query = sanitizePrompt(args.query ?? '');
      if (!query) {
        writeJson(res, 200, mcpTextResult(id, 'Missing required argument: query', true));
        return;
      }
      const text = await hubClient.searchKnowledge(query);
      writeJson(res, 200, mcpTextResult(id, text, false));
      return;
    }

    writeJson(res, 200, mcpTextResult(id, `Unknown tool: ${toolName}`, true));
  } catch (err) {
    writeJson(res, 200, mcpTextResult(id, err?.message || 'Hub request failed', true));
  }
}

export function createXiaoYiCloudServer(cfg, options = {}) {
  if (!cfg?.cloudToken || cfg.cloudToken.length < MIN_CLOUD_TOKEN_LENGTH) {
    throw new Error(`XIAOYI_CLOUD_TOKEN must be at least ${MIN_CLOUD_TOKEN_LENGTH} characters`);
  }

  const hubClient =
    options.hubClient ||
    new PersonaHubClient(
      {
        ...cfg,
        clientHeader: 'harmony-xiaoyi-cloud/1.0.0',
        sourceHeader: 'xiaoyi-cloud'
      },
      options.fetchImpl || globalThis.fetch
    );

  const taskStore = new Map();
  const sessionContexts = new Map();
  let boundOrigin = '';

  const server = http.createServer(async (req, res) => {
    const urlPath = (req.url || '/').split('?')[0];
    const method = (req.method || 'GET').toUpperCase();

    if (urlPath === '/healthz') {
      if (method !== 'GET') {
        writeJson(res, 405, { error: 'Method Not Allowed' });
        return;
      }
      writeJson(res, 200, { ok: true, service: 'persona-xiaoyi-cloud' });
      return;
    }

    if (urlPath === '/.well-known/agent-card.json') {
      if (method !== 'GET') {
        writeJson(res, 405, { error: 'Method Not Allowed' });
        return;
      }
      const protoHeader = String(req.headers['x-forwarded-proto'] || '').split(',')[0].trim().toLowerCase();
      const proto = protoHeader === 'https' ? 'https' : 'http';
      const requestOrigin = req.headers.host ? `${proto}://${req.headers.host}` : boundOrigin;
      writeJson(res, 200, buildAgentCard(cfg, requestOrigin));
      return;
    }

    if (urlPath === '/a2a' || urlPath === '/mcp') {
      if (method !== 'POST') {
        writeJson(res, 405, { error: 'Method Not Allowed' });
        return;
      }

      if (!verifyBearerToken(req.headers.authorization, cfg.cloudToken)) {
        writeJson(
          res,
          401,
          {
            jsonrpc: '2.0',
            id: null,
            error: { code: -32001, message: 'Unauthorized' }
          },
          { 'WWW-Authenticate': 'Bearer' }
        );
        return;
      }

      const bodyResult = await readJsonBody(req, options.maxBodyBytes || MAX_BODY_BYTES);
      if (!bodyResult.ok) {
        if (bodyResult.status === 413) {
          req.resume();
          const destroyTimer = setTimeout(() => {
            if (!req.complete) {
              try { req.destroy(); } catch (_) {}
            }
          }, 2000);
          if (typeof destroyTimer.unref === 'function') destroyTimer.unref();
          writeJson(
            res,
            413,
            {
              jsonrpc: '2.0',
              id: null,
              error: { code: -32000, message: 'Payload Too Large' }
            },
            { Connection: 'close' }
          );
          return;
        }
        writeJson(res, 400, {
          jsonrpc: '2.0',
          id: null,
          error: { code: -32700, message: 'Parse error' }
        });
        return;
      }

      if (urlPath === '/a2a') {
        await handleA2aRequest(bodyResult.value, res, { hubClient, taskStore, sessionContexts });
      } else {
        await handleMcpRequest(bodyResult.value, res, { hubClient });
      }
      return;
    }

    writeJson(res, 404, { error: 'Not Found' });
  });

  return {
    server,
    hubClient,
    taskStore,
    async listen(port = cfg.port ?? DEFAULT_CLOUD_PORT, host = cfg.host || DEFAULT_CLOUD_HOST) {
      await new Promise((resolve, reject) => {
        server.once('error', reject);
        server.listen(port, host, () => {
          server.removeListener('error', reject);
          resolve();
        });
      });
      const addr = server.address();
      const actualPort = typeof addr === 'object' && addr ? addr.port : port;
      const actualHost = typeof addr === 'object' && addr && addr.address ? addr.address : host;
      boundOrigin = `http://${formatHostForUrl(actualHost)}:${actualPort}`;
      return { host: actualHost, port: actualPort, origin: boundOrigin };
    },
    async close() {
      await new Promise((resolve) => server.close(resolve));
    }
  };
}

export async function runCloudPreflightCheck(cfg, { fetchImpl = globalThis.fetch } = {}) {
  const hub = new PersonaHubClient(
    {
      ...cfg,
      clientHeader: 'harmony-xiaoyi-cloud/1.0.0',
      sourceHeader: 'xiaoyi-cloud'
    },
    fetchImpl
  );
  const host = cfg?.host || DEFAULT_CLOUD_HOST;
  const port = cfg?.port !== undefined && cfg?.port !== null ? cfg.port : DEFAULT_CLOUD_PORT;
  const tokenValid = typeof cfg?.cloudToken === 'string' && cfg.cloudToken.length >= MIN_CLOUD_TOKEN_LENGTH;
  const portValid = Number.isInteger(port) && port >= 0 && port <= 65535;
  const hubUrlValid = typeof cfg?.hubUrl === 'string' && /^https?:\/\//i.test(cfg.hubUrl);
  const publicUrlValid = !cfg?.publicBaseUrl || /^https?:\/\//i.test(cfg.publicBaseUrl);
  const configOk = tokenValid && portValid && hubUrlValid && publicUrlValid;

  const report = {
    ok: false,
    hubUrl: cfg?.hubUrl || '',
    listenUrl: `http://${formatHostForUrl(host)}:${port}`,
    publicBaseUrl: cfg?.publicBaseUrl || '',
    checks: {
      config: {
        ok: configOk,
        detail: configOk
          ? `listen=${host}:${port}, bearer=sha256-timingSafe`
          : 'invalid cloudToken (<24 chars), port, or URL'
      },
      hubHealth: { ok: false, detail: '' },
      hubAuth: { ok: false, detail: '' },
      assistant: { ok: false, detail: '' },
      recordingModule: { ok: true, allowed: true, detail: 'on' }
    }
  };

  try {
    const health = await hub.getHealth();
    const recordingAllowed = isHubRecordingAllowed(health?.features);
    report.checks.hubHealth = {
      ok: true,
      detail: `hub=${health?.hub_name || health?.hub_id || 'ok'}${health?.version ? ` v${health.version}` : ''}`
    };
    report.checks.recordingModule = {
      ok: true,
      allowed: recordingAllowed,
      detail: recordingAllowed ? 'on' : 'off (recording requests will fail closed)'
    };
  } catch (err) {
    report.checks.hubHealth = { ok: false, detail: err?.message || 'unreachable' };
  }

  try {
    const machinesRes = await hub.request('/api/v1/machines');
    const machines = Array.isArray(machinesRes?.machines) ? machinesRes.machines : [];
    const online = machines.filter((m) => m.online !== false && m.trust !== 'pending').length;
    report.checks.hubAuth = { ok: true, detail: `authorized (${online}/${machines.length} machines online)` };
  } catch (err) {
    report.checks.hubAuth = {
      ok: false,
      detail: err?.status === 401 || err?.status === 403 ? 'token rejected (HTTP 401/403)' : err?.message || 'failed'
    };
  }

  try {
    const assistantRes = await hub.request('/api/v1/assistant');
    report.checks.assistant = {
      ok: true,
      detail: assistantRes?.main ? 'main conversation ready' : 'ready (will create main on first message)'
    };
  } catch (err) {
    report.checks.assistant = { ok: false, detail: err?.message || 'unavailable' };
  }

  report.ok = Object.values(report.checks).every((c) => c.ok);
  return report;
}

export function formatCloudPreflightReport(report) {
  const lines = [
    `Persona XiaoYi Cloud Preflight (${report.ok ? 'PASS' : 'FAIL'})`,
    `  Hub: ${report.hubUrl}`,
    `  Listen: ${report.listenUrl}`
  ];
  if (report.publicBaseUrl) {
    lines.push(`  PublicBaseUrl: ${report.publicBaseUrl}`);
  }
  for (const [name, entry] of Object.entries(report.checks)) {
    const badge = entry.ok ? 'PASS' : 'FAIL';
    lines.push(`  [${badge}] ${name}: ${entry.detail}`);
  }
  return lines.join('\n');
}

function printUsage() {
  console.log(
    [
      'Persona XiaoYi Cloud A2A v1.0 & Streamable HTTP MCP Server (Node 24, zero dependencies)',
      '',
      'Usage:',
      '  node harmony/scripts/xiaoyi-cloud-server.mjs [--config /path/to/cloud-config.json]',
      '  node harmony/scripts/xiaoyi-cloud-server.mjs --check [--config /path/to/cloud-config.json]',
      '',
      'Required env vars (or JSON keys):',
      '  XIAOYI_CLOUD_TOKEN / cloudToken (>= 24 chars), PERSONA_HUB_URL / hubUrl, PERSONA_HUB_TOKEN / hubToken',
      '',
      'Optional env vars:',
      `  XIAOYI_CLOUD_HOST / host (default: ${DEFAULT_CLOUD_HOST})`,
      `  XIAOYI_CLOUD_PORT / port (default: ${DEFAULT_CLOUD_PORT})`,
      '  XIAOYI_PUBLIC_BASE_URL / publicBaseUrl, PERSONA_MANAGER / manager, XIAOYI_ASK_TIMEOUT_MS / askTimeoutMs, XIAOYI_CONFIG_FILE'
    ].join('\n')
  );
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const cli = parseCloudCliArgs(process.argv.slice(2));
  if (cli.help) {
    printUsage();
    process.exit(0);
  }
  const cfg = loadCloudServerConfig(process.env, cli.configFile);
  if (cli.check) {
    const report = await runCloudPreflightCheck(cfg);
    console.log(formatCloudPreflightReport(report));
    process.exit(report.ok ? 0 : 1);
  }
  const cloud = createXiaoYiCloudServer(cfg);
  const { origin } = await cloud.listen();
  console.log(`[xiaoyi-cloud] listening on ${origin} (a2a=${origin}/a2a, mcp=${origin}/mcp)`);
  const shutdown = async () => {
    await cloud.close();
    process.exit(0);
  };
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);
}
