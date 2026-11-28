#!/usr/bin/env node
/**
 * Standalone, zero-dependency XiaoYi OpenClaw-transport Outbound WebSocket Bridge for Persona Hub.
 *
 * Persona is an independent personal SuperAgent (`personad` Hub), unrelated to OpenClaw; this adapter
 * speaks Huawei XiaoYi Open Platform's outbound WebSocket channel protocol ("OpenClaw mode") so personal
 * Huawei developer accounts can connect a Persona Hub to XiaoYi over outbound WSS.
 *
 * Configuration is supplied strictly via environment variables or a local git-ignored JSON file
 * (--config <path> or XIAOYI_CONFIG_FILE) — never commit credentials, AK/SK, agent IDs, or Hub tokens.
 *
 * Usage:
 *   node harmony/scripts/xiaoyi-openclaw-bridge.mjs [--config /path/to/config.json]
 *   node harmony/scripts/xiaoyi-openclaw-bridge.mjs --check [--check-ws] [--config /path/to/config.json]
 *
 * Required config:
 *   XIAOYI_AK         XiaoYi Open Platform Access Key
 *   XIAOYI_SK         XiaoYi Open Platform Secret Key
 *   XIAOYI_AGENT_ID   XiaoYi OpenClaw Agent ID
 *   PERSONA_HUB_URL   Persona Hub HTTPS/HTTP base URL (e.g. https://hub.example.com)
 *   PERSONA_HUB_TOKEN Persona Hub device/API bearer token
 *
 * Optional config:
 *   XIAOYI_WS_URL         Primary WS endpoint (default: wss://hag.cloud.huawei.com/openclaw/v1/ws/link)
 *   XIAOYI_WS_URL_2       Secondary WS endpoint (default: wss://116.63.174.231/openclaw/v1/ws/link when using default primary; "none" to disable)
 *   XIAOYI_AUTH_MODE      "huawei" (default: x-access-key/x-ts/x-sign/x-agent-id) or "hag"
 *   PERSONA_MANAGER       Optional Persona manager ID header
 *   XIAOYI_HEARTBEAT_MS   Default: 20000
 *   XIAOYI_ASK_TIMEOUT_MS Default: 24000
 */

import { createHash, createHmac, randomBytes, randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import { isIP } from 'node:net';
import { pathToFileURL } from 'node:url';

export const DEFAULT_OPENCLAW_WS_URL = 'wss://hag.cloud.huawei.com/openclaw/v1/ws/link';
export const DEFAULT_OPENCLAW_WS_URL_2 = 'wss://116.63.174.231/openclaw/v1/ws/link';
export const DEFAULT_HEARTBEAT_MS = 20000;
export const DEFAULT_ASK_TIMEOUT_MS = 24000;
export const DEFAULT_STABLE_CONNECTION_MS = 10000;
export const MAX_PROMPT_CHARS = 8000;
const MAX_SESSION_CONTEXTS = 64;
const WS_GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
export const RECORDING_OFF_MESSAGE = 'Meeting recording is off on this hub.';
const HEALTH_INTAKE_ROUTE = '/api/v1/assistant/health/intake?space=health';

export function parseCliArgs(argv = process.argv.slice(2)) {
  const opts = { check: false, checkWs: false, help: false, configFile: '' };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === '--check') opts.check = true;
    else if (arg === '--check-ws') { opts.check = true; opts.checkWs = true; }
    else if (arg === '--help' || arg === '-h') opts.help = true;
    else if (arg === '--config' && i + 1 < argv.length) { opts.configFile = argv[++i]; }
    else if (arg.startsWith('--config=')) { opts.configFile = arg.slice('--config='.length); }
  }
  return opts;
}

/** Returns true strictly for `wss://` URLs targeting a raw IPv4 or IPv6 literal hostname. */
export function isWssIpLiteralUrl(urlString) {
  try {
    const url = new URL(urlString);
    if (url.protocol !== 'wss:') return false;
    const rawHost = url.hostname || '';
    const host = rawHost.startsWith('[') && rawHost.endsWith(']') ? rawHost.slice(1, -1) : rawHost;
    return isIP(host) !== 0;
  } catch (_) {
    return false;
  }
}

/**
 * Zero-dependency RFC 6455 WebSocket client built on `node:http` / `node:https`.
 * Intentionally avoids `Sec-WebSocket-Extensions: permessage-deflate` (which Huawei ELB closes
 * after the first client frame on Node 24's built-in `WebSocket`), supports protocol-level
 * Ping/Pong (`0x9` / `0xA`), and skips TLS cert checks strictly for explicit `wss://` IP literals.
 */
export class Rfc6455WebSocket {
  constructor(urlString, options = {}) {
    this.url = urlString;
    this.readyState = 0; // CONNECTING
    this._listeners = new Map();
    this._socket = null;
    this._req = null;
    this._closedEmitted = false;
    this._closeTimer = null;
    this._recvBuf = Buffer.alloc(0);
    this._fragOpcode = 0;
    this._fragChunks = [];

    let parsed;
    try {
      parsed = new URL(urlString);
    } catch (err) {
      queueMicrotask(() => {
        this._emit('error', err);
        this._emitClose(1006, err?.message || 'invalid url');
      });
      return;
    }

    const isTls = parsed.protocol === 'wss:';
    if (!isTls && parsed.protocol !== 'ws:') {
      const err = new Error(`Unsupported WebSocket protocol: ${parsed.protocol}`);
      queueMicrotask(() => {
        this._emit('error', err);
        this._emitClose(1006, err.message);
      });
      return;
    }

    const secKey = randomBytes(16).toString('base64');
    const reqHeaders = {};
    if (options.headers && typeof options.headers === 'object') {
      for (const [k, v] of Object.entries(options.headers)) {
        if (k.toLowerCase() === 'sec-websocket-extensions') continue;
        if (v !== undefined && v !== null) reqHeaders[k] = String(v);
      }
    }
    reqHeaders.Connection = 'Upgrade';
    reqHeaders.Upgrade = 'websocket';
    reqHeaders['Sec-WebSocket-Key'] = secKey;
    reqHeaders['Sec-WebSocket-Version'] = '13';

    const transport = isTls ? https : http;
    const reqOpts = {
      protocol: isTls ? 'https:' : 'http:',
      hostname: parsed.hostname,
      port: parsed.port || (isTls ? 443 : 80),
      path: `${parsed.pathname || '/'}${parsed.search || ''}`,
      method: 'GET',
      headers: reqHeaders
    };
    if (isTls) {
      reqOpts.rejectUnauthorized = options.rejectUnauthorized !== undefined
        ? Boolean(options.rejectUnauthorized)
        : !isWssIpLiteralUrl(urlString);
    }

    const req = transport.request(reqOpts);
    this._req = req;

    req.on('upgrade', (res, socket, head) => {
      const expectedAccept = createHash('sha1').update(`${secKey}${WS_GUID}`).digest('base64');
      const actualAccept = String(res.headers['sec-websocket-accept'] || '').trim();
      if (actualAccept !== expectedAccept) {
        const err = new Error('Invalid Sec-WebSocket-Accept header from server');
        socket.destroy();
        this._emit('error', err);
        this._emitClose(1006, err.message);
        return;
      }

      this._socket = socket;
      this.readyState = 1; // OPEN
      if (typeof socket.setNoDelay === 'function') socket.setNoDelay(true);

      socket.on('data', (chunk) => this._onData(chunk));
      socket.on('error', (err) => {
        this._emit('error', err);
      });
      socket.on('close', () => {
        this._emitClose(this._pendingCloseCode ?? 1006, this._pendingCloseReason ?? '');
      });
      socket.on('end', () => {
        if (!this._closedEmitted && this._pendingCloseCode !== undefined) {
          this._emitClose(this._pendingCloseCode, this._pendingCloseReason ?? '');
        }
      });

      this._emit('open', {});
      if (head && head.length > 0) {
        this._onData(head);
      }
    });

    req.on('response', (res) => {
      res.resume();
      const err = new Error(`WebSocket upgrade failed with HTTP ${res.statusCode}`);
      this._emit('error', err);
      this._emitClose(1006, err.message);
    });

    req.on('error', (err) => {
      if (!this._socket) {
        this._emit('error', err);
        this._emitClose(1006, err?.message || 'socket error');
      }
    });

    req.end();
  }

  addEventListener(type, fn) {
    if (typeof fn !== 'function') return;
    let set = this._listeners.get(type);
    if (!set) {
      set = new Set();
      this._listeners.set(type, set);
    }
    set.add(fn);
  }

  removeEventListener(type, fn) {
    const set = this._listeners.get(type);
    if (set) set.delete(fn);
  }

  _emit(type, event) {
    const set = this._listeners.get(type);
    if (!set) return;
    for (const fn of Array.from(set)) {
      try {
        fn(event);
      } catch (_) {
        // Keep listener dispatch resilient.
      }
    }
  }

  _emitClose(code, reason) {
    if (this._closedEmitted) return;
    this._closedEmitted = true;
    this.readyState = 3; // CLOSED
    if (this._closeTimer) {
      clearTimeout(this._closeTimer);
      this._closeTimer = null;
    }
    this._emit('close', { code, reason });
  }

  _sendFrame(opcode, payloadBuf) {
    if (!this._socket || this._socket.destroyed || !this._socket.writable) return;
    const payload = Buffer.isBuffer(payloadBuf) ? payloadBuf : Buffer.from(payloadBuf || '');
    const len = payload.length;
    const mask = randomBytes(4);
    let headerLen = 2;
    if (len >= 126 && len < 65536) headerLen = 4;
    else if (len >= 65536) headerLen = 10;

    const frame = Buffer.allocUnsafe(headerLen + 4 + len);
    frame[0] = 0x80 | (opcode & 0x0f);
    if (len < 126) {
      frame[1] = 0x80 | len;
    } else if (len < 65536) {
      frame[1] = 0x80 | 126;
      frame.writeUInt16BE(len, 2);
    } else {
      frame[1] = 0x80 | 127;
      frame.writeBigUInt64BE(BigInt(len), 2);
    }
    mask.copy(frame, headerLen);
    for (let i = 0; i < len; i++) {
      frame[headerLen + 4 + i] = payload[i] ^ mask[i & 3];
    }
    this._socket.write(frame);
  }

  send(data) {
    if (this.readyState !== 1) return;
    if (Buffer.isBuffer(data) || data instanceof Uint8Array) {
      this._sendFrame(0x2, Buffer.from(data));
    } else {
      this._sendFrame(0x1, Buffer.from(String(data), 'utf8'));
    }
  }

  ping(data = '') {
    if (this.readyState !== 1) return;
    const buf = Buffer.isBuffer(data) ? data : Buffer.from(String(data), 'utf8');
    this._sendFrame(0x9, buf);
  }

  close(code = 1000, reason = '') {
    if (this.readyState === 2 || this.readyState === 3) return;
    if (this.readyState === 0) {
      this.readyState = 3;
      try { this._req?.destroy(); } catch (_) {}
      this._emitClose(code, reason);
      return;
    }
    this.readyState = 2; // CLOSING
    this._pendingCloseCode = code;
    this._pendingCloseReason = reason;
    try {
      const reasonBuf = Buffer.from(String(reason || ''), 'utf8').subarray(0, 123);
      const body = Buffer.allocUnsafe(2 + reasonBuf.length);
      body.writeUInt16BE(Number(code) || 1000, 0);
      reasonBuf.copy(body, 2);
      this._sendFrame(0x8, body);
      this._socket?.end();
    } catch (_) {
      try { this._socket?.destroy(); } catch (_) {}
    }
    this._closeTimer = setTimeout(() => {
      try { this._socket?.destroy(); } catch (_) {}
      this._emitClose(code, reason);
    }, 500);
    if (typeof this._closeTimer.unref === 'function') this._closeTimer.unref();
  }

  _onData(chunk) {
    this._recvBuf = this._recvBuf.length > 0 ? Buffer.concat([this._recvBuf, chunk]) : chunk;
    while (this._recvBuf.length >= 2) {
      const b0 = this._recvBuf[0];
      const b1 = this._recvBuf[1];
      const fin = (b0 & 0x80) !== 0;
      const opcode = b0 & 0x0f;
      const masked = (b1 & 0x80) !== 0;
      let payloadLen = b1 & 0x7f;
      let offset = 2;

      if (payloadLen === 126) {
        if (this._recvBuf.length < 4) return;
        payloadLen = this._recvBuf.readUInt16BE(2);
        offset = 4;
      } else if (payloadLen === 127) {
        if (this._recvBuf.length < 10) return;
        payloadLen = Number(this._recvBuf.readBigUInt64BE(2));
        offset = 10;
      }

      const maskLen = masked ? 4 : 0;
      if (this._recvBuf.length < offset + maskLen + payloadLen) return;

      const mask = masked ? this._recvBuf.subarray(offset, offset + 4) : null;
      const payload = Buffer.from(this._recvBuf.subarray(offset + maskLen, offset + maskLen + payloadLen));
      this._recvBuf = this._recvBuf.subarray(offset + maskLen + payloadLen);

      if (mask) {
        for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i & 3];
      }

      if (opcode === 0x8) {
        let closeCode = 1005;
        let closeReason = '';
        if (payload.length >= 2) {
          closeCode = payload.readUInt16BE(0);
          closeReason = payload.subarray(2).toString('utf8');
        }
        this._pendingCloseCode = closeCode;
        this._pendingCloseReason = closeReason;
        if (this.readyState === 1) {
          this.readyState = 2;
          try {
            const reply = payload.length >= 2 ? payload.subarray(0, 2) : Buffer.alloc(0);
            this._sendFrame(0x8, reply);
            this._socket?.end();
          } catch (_) {}
        }
        this._emitClose(closeCode, closeReason);
        return;
      }

      if (opcode === 0x9) {
        // RFC 6455 Ping -> automatic Pong with identical payload
        if (this.readyState === 1) this._sendFrame(0xA, payload);
        this._emit('ping', { data: payload });
        continue;
      }

      if (opcode === 0xA) {
        this._emit('pong', { data: payload });
        continue;
      }

      if (opcode === 0x1 || opcode === 0x2) {
        if (fin) {
          this._emit('message', { data: opcode === 0x1 ? payload.toString('utf8') : payload });
        } else {
          this._fragOpcode = opcode;
          this._fragChunks = [payload];
        }
      } else if (opcode === 0x0 && this._fragOpcode) {
        this._fragChunks.push(payload);
        if (fin) {
          const full = Buffer.concat(this._fragChunks);
          const msgOpcode = this._fragOpcode;
          this._fragOpcode = 0;
          this._fragChunks = [];
          this._emit('message', { data: msgOpcode === 0x1 ? full.toString('utf8') : full });
        }
      }
    }
  }
}

export function loadBridgeConfig(env = process.env, configFileOverride = '') {
  let fileCfg = {};
  const configPath = configFileOverride || env.XIAOYI_CONFIG_FILE || '';
  if (configPath) {
    const raw = readFileSync(configPath, 'utf8');
    fileCfg = JSON.parse(raw);
  }
  const pick = (envKey, fileKey, fallback = '') => {
    const val = env[envKey] !== undefined && env[envKey] !== '' ? env[envKey] : fileCfg[fileKey];
    return typeof val === 'string' ? val.trim() : fallback;
  };
  const authMode = pick('XIAOYI_AUTH_MODE', 'authMode', 'huawei').toLowerCase();
  const wsUrl = pick('XIAOYI_WS_URL', 'wsUrl') || pick('XIAOYI_WS_URL_1', 'wsUrl1') || DEFAULT_OPENCLAW_WS_URL;

  const hasEnvWs2 = env.XIAOYI_WS_URL_2 !== undefined;
  const hasFileWs2 = fileCfg.wsUrl2 !== undefined;
  let wsUrl2 = '';
  if (hasEnvWs2 || hasFileWs2) {
    const rawWs2 = String(hasEnvWs2 ? env.XIAOYI_WS_URL_2 : fileCfg.wsUrl2).trim();
    if (rawWs2 && !['none', 'false', 'off'].includes(rawWs2.toLowerCase())) {
      wsUrl2 = rawWs2;
    }
  } else if (wsUrl === DEFAULT_OPENCLAW_WS_URL) {
    wsUrl2 = DEFAULT_OPENCLAW_WS_URL_2;
  }

  const cfg = {
    wsUrl,
    wsUrl2,
    authMode: authMode === 'hag' ? 'hag' : 'huawei',
    ak: pick('XIAOYI_AK', 'ak'),
    sk: pick('XIAOYI_SK', 'sk'),
    agentId: pick('XIAOYI_AGENT_ID', 'agentId'),
    hubUrl: pick('PERSONA_HUB_URL', 'hubUrl').replace(/\/+$/, ''),
    hubToken: pick('PERSONA_HUB_TOKEN', 'hubToken'),
    manager: pick('PERSONA_MANAGER', 'manager'),
    heartbeatMs: Number(env.XIAOYI_HEARTBEAT_MS || fileCfg.heartbeatMs || DEFAULT_HEARTBEAT_MS),
    askTimeoutMs: Number(env.XIAOYI_ASK_TIMEOUT_MS || fileCfg.askTimeoutMs || DEFAULT_ASK_TIMEOUT_MS)
  };
  const missing = [];
  if (!cfg.ak) missing.push('XIAOYI_AK');
  if (!cfg.sk) missing.push('XIAOYI_SK');
  if (!cfg.agentId) missing.push('XIAOYI_AGENT_ID');
  if (!cfg.hubUrl) missing.push('PERSONA_HUB_URL');
  if (!cfg.hubToken) missing.push('PERSONA_HUB_TOKEN');
  if (missing.length > 0) {
    throw new Error(`Missing required configuration: ${missing.join(', ')}`);
  }
  if (!/^wss?:\/\//i.test(cfg.wsUrl)) {
    throw new Error('XIAOYI_WS_URL must use ws:// or wss://');
  }
  if (cfg.wsUrl2 && !/^wss?:\/\//i.test(cfg.wsUrl2)) {
    throw new Error('XIAOYI_WS_URL_2 must use ws:// or wss://');
  }
  if (!/^https?:\/\//i.test(cfg.hubUrl)) {
    throw new Error('PERSONA_HUB_URL must use http:// or https://');
  }
  if (!Number.isFinite(cfg.heartbeatMs) || cfg.heartbeatMs < 1000) {
    cfg.heartbeatMs = DEFAULT_HEARTBEAT_MS;
  }
  if (!Number.isFinite(cfg.askTimeoutMs) || cfg.askTimeoutMs < 1000) {
    cfg.askTimeoutMs = DEFAULT_ASK_TIMEOUT_MS;
  }
  return cfg;
}

/**
 * Build HMAC-SHA256 authentication headers for XiaoYi OpenClaw outbound WebSocket handshake.
 * - "huawei" (default): official Huawei OpenClaw headers (`x-access-key`, `x-ts`, `x-sign`, `x-agent-id`).
 * - "hag": legacy custom gateway headers (`x-hag-ak`, `x-hag-agent-id`, `x-hag-timestamp`, `x-hag-nonce`, `x-hag-signature`).
 */
export function signOpenClawHeaders({
  ak,
  sk,
  agentId,
  authMode = 'huawei',
  timestampMs = Date.now(),
  nonce = randomBytes(12).toString('hex')
}) {
  const ts = String(Math.floor(timestampMs));
  if (authMode === 'hag') {
    const canonical = `${ak}\n${agentId}\n${ts}\n${nonce}`;
    const signature = createHmac('sha256', sk).update(canonical, 'utf8').digest('base64');
    return {
      'x-hag-ak': ak,
      'x-hag-agent-id': agentId,
      'x-hag-timestamp': ts,
      'x-hag-nonce': nonce,
      'x-hag-signature': signature
    };
  }
  const sign = createHmac('sha256', sk).update(ts, 'utf8').digest('base64');
  return {
    'x-access-key': ak,
    'x-ts': ts,
    'x-sign': sign,
    'x-agent-id': agentId
  };
}

/** Exponential backoff delay: 1s, 2s, 4s, 8s, 16s, capped at 30s. */
export function computeReconnectDelayMs(attempt) {
  const safe = Math.max(0, Math.floor(Number(attempt) || 0));
  return Math.min(30000, 1000 * Math.pow(2, Math.min(5, safe)));
}

/** Follow upstream HubModules (#15978 / #15985): off only when `modules` is announced without `module.recording`. */
export function isHubRecordingAllowed(features) {
  if (!Array.isArray(features) || features.length === 0) return true;
  if (!features.includes('modules')) return true;
  return features.includes('module.recording');
}

export function sanitizePrompt(raw) {
  const cleaned = String(raw || '').replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, '').trim();
  const chars = Array.from(cleaned);
  return chars.length <= MAX_PROMPT_CHARS ? cleaned : chars.slice(0, MAX_PROMPT_CHARS).join('');
}

export function extractMessageText(params) {
  const msg = params?.message || params;
  const parts = Array.isArray(msg?.parts) ? msg.parts : [];
  const chunks = [];
  for (const part of parts) {
    if (typeof part?.text === 'string' && part.text.trim()) chunks.push(part.text.trim());
    if (typeof part?.url === 'string' && /^https?:\/\//i.test(part.url.trim())) chunks.push(part.url.trim());
    if (part?.kind === 'file' && part?.file) {
      if (typeof part.file.uri === 'string' && /^https?:\/\//i.test(part.file.uri.trim())) chunks.push(part.file.uri.trim());
      else if (typeof part.file.name === 'string' && part.file.name.trim()) chunks.push(`[File: ${part.file.name.trim()}]`);
    }
  }
  if (chunks.length === 0 && typeof params?.text === 'string') chunks.push(params.text);
  if (chunks.length === 0 && typeof params?.query === 'string') chunks.push(params.query);
  return sanitizePrompt(chunks.join('\n'));
}

export function classifyRelayCommand(text) {
  // Security boundary: NEVER execute approvals silently over the cloud OpenClaw relay.
  if (/^(?:approve|allow\s+first|批准|通过待确认|审批|確認待ちを承認)/i.test(text)) {
    return { kind: 'refuse_approval', arg: '' };
  }
  // Recording boundary: microphone capture is on-device and gated by Hub `module.recording` (#15985).
  if (/^(?:start\s+(?:a\s+)?(?:meeting\s+)?recording|record\s+(?:a\s+)?meeting|开始(?:会议)?录音|用\s*persona\s*录音|会議を録音|録音を開始)/i.test(text)) {
    return { kind: 'start_recording', arg: '' };
  }
  if (/^(?:log\s+water|drink\s+water|记喝水|喝了一杯水|喝水|记录喝水|水分を記録)/i.test(text)) {
    const mlMatch = /(\d{1,4})\s*(?:ml|毫升|ミリリットル)?/i.exec(text);
    const parsedMl = mlMatch ? Number(mlMatch[1]) : undefined;
    const validMl = parsedMl !== undefined && parsedMl >= 1 && parsedMl <= 3000 ? parsedMl : undefined;
    return { kind: 'log_water', arg: '', amountMl: validMl };
  }
  const agentMatch = /^(?:send\s+to\s+agent|ask\s+claude|have\s+(?:an\s+)?agent|让\s*agent\s*(?:去做)?|交给\s*agent|エージェントに依頼)\s*[:：]?\s*(.+)$/i.exec(text);
  if (agentMatch && agentMatch[1].trim()) {
    const rawArg = agentMatch[1].trim();
    const scoped = /^on\s+([^:：\n]+?)\s*[:：]\s*(.+)$/i.exec(rawArg);
    if (scoped && scoped[1].trim() && scoped[2].trim()) {
      return { kind: 'send_agent', arg: scoped[2].trim(), machineName: scoped[1].trim() };
    }
    return { kind: 'send_agent', arg: rawArg, machineName: '' };
  }
  const todoMatch = /^(?:add\s+(?:a\s+)?to-?do|new\s+to-?do|新建待办|记个待办|待办|ToDoを追加)\s*[:：]?\s*(.+)$/i.exec(text);
  if (todoMatch && todoMatch[1].trim()) {
    return { kind: 'add_todo', arg: todoMatch[1].trim() };
  }
  const kbMatch = /^(?:search\s+(?:persona\s+)?knowledge|搜索知识库|查知识库|ナレッジ検索)\s*[:：]?\s*(.+)$/i.exec(text);
  if (kbMatch && kbMatch[1].trim()) {
    return { kind: 'search_kb', arg: kbMatch[1].trim() };
  }
  if (/^(?:persona\s+status|check\s+status|status|查看状态|任务进度|待确认|状況確認)/i.test(text)) {
    return { kind: 'status', arg: '' };
  }
  return { kind: 'ask', arg: text };
}

export class PersonaHubClient {
  constructor(cfg, fetchImpl = globalThis.fetch) {
    this.hubUrl = cfg.hubUrl;
    this.hubToken = cfg.hubToken;
    this.manager = cfg.manager || '';
    this.askTimeoutMs = cfg.askTimeoutMs || DEFAULT_ASK_TIMEOUT_MS;
    this.clientHeader = cfg.clientHeader || 'harmony-xiaoyi-openclaw/1.0.0';
    this.sourceHeader = cfg.sourceHeader || 'xiaoyi-openclaw';
    this.fetch = fetchImpl;
  }

  async request(path, method = 'GET', body = undefined, opId = '', { includeManager = true } = {}) {
    const headers = {
      Authorization: `Bearer ${this.hubToken}`,
      'X-Persona-Client': this.clientHeader,
      'X-Persona-Source': this.sourceHeader
    };
    if (includeManager && this.manager) headers['X-Persona-Manager'] = this.manager;
    if (opId) headers['X-Persona-Op-Id'] = opId;
    if (body !== undefined) headers['Content-Type'] = 'application/json';

    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 15000);
    try {
      const res = await this.fetch(`${this.hubUrl}${path}`, {
        method,
        headers,
        body: body !== undefined ? JSON.stringify(body) : undefined,
        signal: controller.signal
      });
      const text = await res.text();
      if (!res.ok) {
        const err = new Error(`Hub HTTP ${res.status}`);
        err.status = res.status;
        err.body = text.slice(0, 512);
        throw err;
      }
      return text.trim() ? JSON.parse(text) : {};
    } finally {
      clearTimeout(timer);
    }
  }

  async getHealth() {
    return this.request('/api/v1/health');
  }

  async isRecordingAllowed() {
    try {
      const health = await this.getHealth();
      return isHubRecordingAllowed(health?.features);
    } catch (_) {
      return true;
    }
  }

  async checkStatus() {
    const [sessionsRes, interRes] = await Promise.all([
      this.request('/api/v1/sessions'),
      this.request('/api/v1/interactions')
    ]);
    const sessions = Array.isArray(sessionsRes.sessions) ? sessionsRes.sessions : [];
    const interactions = Array.isArray(interRes.interactions) ? interRes.interactions : [];
    const working = sessions.filter((s) => !s.assistant && (s.state === 'working' || s.status === 'working')).length;
    const open = interactions.filter((i) => {
      const inner = i.interaction || i;
      const st = String(i.status || inner.status || 'open').toLowerCase();
      return (st === 'open' || st === 'pending') && !inner.quiet && !inner.muted && !(inner.delegating && inner.kind !== 'question');
    });
    if (working === 0 && open.length === 0) {
      return 'All clear. Nothing is running or needs your attention right now.';
    }
    const parts = [];
    if (working > 0) parts.push(`${working} working`);
    if (open.length > 0) parts.push(`${open.length} need you`);
    const top = open[0] ? (open[0].interaction || open[0]) : null;
    const detail = top ? ` First pending: ${top.summary || top.tool_name || top.kind}. Confirm in Persona App.` : '';
    return `${parts.join(', ')}.${detail}`;
  }

  async addTodo(title) {
    const opId = randomUUID();
    const res = await this.request('/api/v1/todos', 'POST', { title }, opId);
    const savedTitle = res?.todo?.title || title;
    return `To-do saved: ${savedTitle}`;
  }

  async logWater(amountMl) {
    if (amountMl !== undefined && (!Number.isFinite(amountMl) || amountMl < 1 || amountMl > 3000)) {
      throw new Error('Water amount must be between 1 and 3000 ml.');
    }
    const state = await this.request(HEALTH_INTAKE_ROUTE, 'GET', undefined, '', { includeManager: false });
    if (Array.isArray(state.items) && state.items.length > 0 && !state.items.includes('water')) {
      throw new Error('First add Water tracking in Health inside Persona.');
    }
    const goalMl = Number(state?.water?.goal || 0);
    const entryId = randomUUID();
    const body = { id: entryId, kind: 'water', at: new Date().toISOString() };
    if (amountMl !== undefined) body.amount = amountMl;
    const posted = await this.request(HEALTH_INTAKE_ROUTE, 'POST', body, entryId, { includeManager: false });
    const recordedMl = Number(posted?.entry?.amount ?? amountMl ?? 0);
    const todayMl = Number(posted?.today?.water ?? 0);
    return `Logged ${recordedMl} ml. Today: ${todayMl} / ${goalMl} ml.`;
  }

  async sendAgentTask(prompt, machineName = '', sessionName = '') {
    const [machinesRes, sessionsRes] = await Promise.all([
      this.request('/api/v1/machines'),
      this.request('/api/v1/sessions')
    ]);
    const machines = (Array.isArray(machinesRes.machines) ? machinesRes.machines : []).filter(
      (m) => m && m.id && m.trust !== 'pending' && !m.pending_confirm && !m.pending
    );
    const query = String(machineName || '').trim();
    let pickedMachine;
    if (query) {
      const lowered = query.toLowerCase();
      pickedMachine = machines.find((m) => m.id === query) ||
        machines.find((m) => String(m.name || '').toLowerCase() === lowered) ||
        machines.find((m) => String(m.name || '').toLowerCase().includes(lowered) || String(m.id).toLowerCase().includes(lowered));
      if (!pickedMachine) throw new Error(`Could not find a computer named ${query}.`);
      if (pickedMachine.online === false) throw new Error(`${pickedMachine.name || pickedMachine.id} is offline.`);
    } else {
      pickedMachine = machines.find((m) => m.online !== false);
      if (!pickedMachine) throw new Error('No computer is online right now.');
    }

    const sessions = (Array.isArray(sessionsRes.sessions) ? sessionsRes.sessions : []).filter((s) => {
      if (!s) return false;
      const mId = s.machine_id || s.machine || '';
      if (mId !== pickedMachine.id || s.assistant) return false;
      const status = String(s.status || '').toLowerCase();
      const state = String(s.state || '').toLowerCase();
      if (status === 'offline' || state === 'offline') return false;
      const readOnly = Boolean(s.read_only) || s.source_kind === 'identity_conflict';
      const canSend = !readOnly && (!s.source_kind || Boolean(s.capabilities?.send));
      return canSend;
    }).sort((a, b) => {
      const ta = Date.parse(a.updated_at || '');
      const tb = Date.parse(b.updated_at || '');
      const va = Number.isFinite(ta) ? ta : 0;
      const vb = Number.isFinite(tb) ? tb : 0;
      if (va !== vb) return vb - va;
      const ia = String(a.session_id || a.id || '');
      const ib = String(b.session_id || b.id || '');
      return ia < ib ? -1 : ia > ib ? 1 : 0;
    });

    const sQuery = String(sessionName || '').trim();
    let pickedSession;
    if (sQuery) {
      const sLower = sQuery.toLowerCase();
      pickedSession = sessions.find((s) => (s.session_id || s.id) === sQuery) ||
        sessions.find((s) => String(s.title || s.name || '').toLowerCase() === sLower) ||
        sessions.find((s) => String(s.title || s.name || '').toLowerCase().includes(sLower));
    } else {
      pickedSession = sessions[0];
    }
    if (!pickedSession) {
      throw new Error(`No active session found on ${pickedMachine.name || pickedMachine.id}. Open Persona to start one.`);
    }

    const sessionId = pickedSession.session_id || pickedSession.id;
    const sessionTitle = pickedSession.title || pickedSession.name || sessionId;
    const machineLabel = pickedMachine.name || pickedMachine.id;
    const opId = randomUUID();
    await this.request(
      `/api/v1/sessions/${encodeURIComponent(pickedMachine.id)}/${encodeURIComponent(sessionId)}/messages`,
      'POST',
      { text: prompt, client_msg_id: opId },
      opId
    );
    return `Sent to ${sessionTitle} (${machineLabel}).`;
  }

  async searchKnowledge(query) {
    const res = await this.request(`/api/v1/kb-search?q=${encodeURIComponent(query)}&messages=0`);
    const hits = Array.isArray(res.hits) ? res.hits : [];
    if (hits.length === 0) return `No knowledge matches found for "${query}".`;
    return hits.slice(0, 5).map((h) => `${h.path}${h.line ? `: ${h.line}` : ''}`).join('\n');
  }

  async interrupt(machineId, sessionId) {
    if (!machineId || !sessionId) return;
    try {
      await this.request(`/api/v1/sessions/${encodeURIComponent(machineId)}/${encodeURIComponent(sessionId)}/interrupt`, 'POST', {});
    } catch (_) {
      // Best-effort cancellation.
    }
  }

  async resolveMainConversation() {
    const assistantRes = await this.request('/api/v1/assistant');
    let sessionId = assistantRes.main || '';
    let machineId = assistantRes.main_machine_id || assistantRes.machine_id || '';
    if (!machineId && sessionId && Array.isArray(assistantRes.conversations)) {
      const mainRow = assistantRes.conversations.find((c) => (c.id || c.session_id) === sessionId);
      if (mainRow) machineId = mainRow.machine_id || mainRow.machine || '';
    }
    if (!sessionId || !machineId) {
      const created = await this.request('/api/v1/assistant/conversations', 'POST', { main: true, prompt: '' }, randomUUID());
      sessionId = created?.session?.id || created?.session?.session_id || '';
      machineId = created?.session?.machine_id || created?.session?.machine || '';
    }
    if (!sessionId || !machineId) {
      throw new Error('No main Persona conversation available.');
    }
    return { machineId, sessionId };
  }

  async askPersona(question, { onChunk, isCancelled, onTarget, conversationId = '', machineId: existingMachineId = '' } = {}) {
    let machineId = existingMachineId;
    let sessionId = conversationId;
    const usedCached = Boolean(machineId && sessionId);
    if (!machineId || !sessionId) {
      ({ machineId, sessionId } = await this.resolveMainConversation());
    }
    if (onTarget) onTarget({ machineId, sessionId });

    const readBaseline = async (m, s) => {
      const baseItems = `/api/v1/sessions/${encodeURIComponent(m)}/${encodeURIComponent(s)}/items`;
      let seq = 0;
      try {
        const initial = await this.request(`${baseItems}?lines=20&fold_work=1&lazy_images=1`);
        for (const item of Array.isArray(initial.items) ? initial.items : []) {
          if (typeof item.seq === 'number' && item.seq > seq) seq = item.seq;
        }
      } catch (_) {
        // Fresh conversation.
      }
      return { baseItems, baselineSeq: seq };
    };

    let { baseItems, baselineSeq } = await readBaseline(machineId, sessionId);
    const opId = randomUUID();
    try {
      await this.request(
        `/api/v1/sessions/${encodeURIComponent(machineId)}/${encodeURIComponent(sessionId)}/messages`,
        'POST',
        { text: question, client_msg_id: opId },
        opId
      );
    } catch (err) {
      if (usedCached && err?.status === 404) {
        ({ machineId, sessionId } = await this.resolveMainConversation());
        if (onTarget) onTarget({ machineId, sessionId });
        ({ baseItems, baselineSeq } = await readBaseline(machineId, sessionId));
        const retryOpId = randomUUID();
        await this.request(
          `/api/v1/sessions/${encodeURIComponent(machineId)}/${encodeURIComponent(sessionId)}/messages`,
          'POST',
          { text: question, client_msg_id: retryOpId },
          retryOpId
        );
      } else {
        throw err;
      }
    }

    const deadline = Date.now() + this.askTimeoutMs;
    let emitted = '';
    while (Date.now() < deadline) {
      if (isCancelled && isCancelled()) {
        await this.interrupt(machineId, sessionId);
        return { text: '', canceled: true, machineId, sessionId };
      }
      await new Promise((r) => setTimeout(r, 250));
      if (isCancelled && isCancelled()) {
        await this.interrupt(machineId, sessionId);
        return { text: '', canceled: true, machineId, sessionId };
      }
      const polled = await this.request(`${baseItems}?after_seq=${baselineSeq}&lines=80&fold_work=1&lazy_images=1`);
      const items = Array.isArray(polled.items) ? polled.items : [];
      const texts = items
        .filter((i) => i.seq > baselineSeq && i.kind === 'text' && i.role === 'assistant' && typeof i.text === 'string' && i.text.trim())
        .map((i) => i.text.trim());
      const combined = texts.join('\n\n');
      if (combined.length > emitted.length) {
        const delta = combined.slice(emitted.length);
        emitted = combined;
        if (onChunk) onChunk(delta, false);
      }
      const sessionsRes = await this.request('/api/v1/sessions');
      const live = (Array.isArray(sessionsRes.sessions) ? sessionsRes.sessions : []).find(
        (s) => (s.machine_id || s.machine) === machineId && (s.session_id || s.id) === sessionId
      );
      const stillWorking = live ? (live.state === 'working' || live.status === 'working') : false;
      if (!stillWorking && emitted) {
        if (onChunk) onChunk('', true);
        return { text: emitted, canceled: false, machineId, sessionId };
      }
    }
    return {
      text: emitted || 'Sent to Persona. It is still thinking — open Persona to view progress.',
      canceled: false,
      machineId,
      sessionId
    };
  }
}

function rememberSessionContext(sessionContexts, sessionKey, binding) {
  if (!sessionKey || !binding?.machineId || !binding?.sessionId) return;
  sessionContexts.delete(sessionKey);
  sessionContexts.set(sessionKey, binding);
  while (sessionContexts.size > MAX_SESSION_CONTEXTS) {
    const oldest = sessionContexts.keys().next().value;
    sessionContexts.delete(oldest);
  }
}

/**
 * Unwrap an incoming WebSocket message that may be any of:
 * 1. Huawei XiaoYi OpenClaw envelope (`{ msgType, agentId, sessionId, taskId, msgDetail }`),
 * 2. Huawei XiaoYi live OpenClaw JSON-RPC frame with top-level `agentId` (`{ agentId, jsonrpc: "2.0", id, method, params }`),
 * 3. Bare JSON-RPC 2.0 frame (`{ jsonrpc: "2.0", id, method, params }`) without `agentId`.
 */
export function unwrapOpenClawEnvelope(rawFrame, defaultAgentId = '') {
  if (!rawFrame || typeof rawFrame !== 'object') return { ignore: true };
  if (typeof rawFrame.msgType === 'string') {
    const type = rawFrame.msgType;
    if (type === 'heartbeat' || type === 'pong' || type === 'clawd_bot_init_ack' || type === 'init_ack') {
      return { ignore: true, msgType: type };
    }
    let detail = rawFrame.msgDetail;
    if (typeof detail === 'string') {
      try { detail = JSON.parse(detail); } catch (_) { return { ignore: true }; }
    }
    if (!detail || typeof detail !== 'object') return { ignore: true };
    return {
      ignore: false,
      enveloped: true,
      envelope: {
        agentId: String(rawFrame.agentId || defaultAgentId || ''),
        sessionId: String(rawFrame.sessionId || detail?.params?.sessionId || detail?.sessionId || ''),
        taskId: String(rawFrame.taskId || detail?.params?.id || detail?.params?.taskId || detail?.id || '')
      },
      rpcFrame: detail
    };
  }
  if (typeof rawFrame.agentId === 'string' && rawFrame.agentId) {
    return {
      ignore: false,
      enveloped: true,
      envelope: {
        agentId: String(rawFrame.agentId || defaultAgentId || ''),
        sessionId: String(rawFrame?.params?.sessionId || rawFrame.sessionId || ''),
        taskId: String(rawFrame?.params?.id || rawFrame?.params?.taskId || rawFrame.taskId || rawFrame.id || '')
      },
      rpcFrame: rawFrame
    };
  }
  return { ignore: false, enveloped: false, envelope: null, rpcFrame: rawFrame };
}

function formatEnvelopedStatusUpdate(id, taskId, state, text) {
  return {
    jsonrpc: '2.0',
    id,
    result: {
      taskId,
      kind: 'status-update',
      final: false,
      status: {
        message: {
          role: 'agent',
          parts: [{ kind: 'text', text }]
        },
        state
      }
    }
  };
}

function formatEnvelopedArtifactUpdate(id, taskId, parts, { append = false, lastChunk = true, final = false } = {}) {
  return {
    jsonrpc: '2.0',
    id,
    result: {
      taskId,
      kind: 'artifact-update',
      append,
      lastChunk,
      final,
      artifact: {
        artifactId: randomUUID(),
        parts
      }
    }
  };
}

/** Handle one incoming JSON-RPC 2.0 frame (bare or unwrapped from Huawei OpenClaw envelope). */
export async function handleOpenClawRpc(
  frame,
  hubClient,
  sendFrame,
  activeTasks = new Map(),
  { enveloped = false, envelope = null, sessionContexts = new Map() } = {}
) {
  const id = frame?.id ?? null;
  const method = String(frame?.method || frame?.action || '');
  const params = frame?.params || {};
  const sessionKey = String(envelope?.sessionId || params.sessionId || frame?.sessionId || params.contextId || '');

  const emit = (rpcPayload, explicitTaskId = '') => {
    if (!enveloped) {
      sendFrame(rpcPayload);
      return;
    }
    sendFrame({
      msgType: 'agent_response',
      agentId: envelope?.agentId || '',
      sessionId: sessionKey,
      taskId: explicitTaskId || envelope?.taskId || '',
      msgDetail: JSON.stringify(rpcPayload)
    });
  };

  if (method === 'ping' || method === 'heartbeat') {
    emit({ jsonrpc: '2.0', id, result: { pong: true, timestamp: Date.now() } });
    return;
  }

  if (method === 'clearContext' || method === 'clear_context' || method === 'clear' || method === 'context/clear') {
    if (sessionKey) sessionContexts.delete(sessionKey);
    if (enveloped) {
      emit({
        jsonrpc: '2.0',
        id,
        result: { status: { state: 'cleared' } }
      }, String(frame?.taskId || envelope?.taskId || id || ''));
    } else {
      emit({
        jsonrpc: '2.0',
        id,
        result: { cleared: true, sessionId: sessionKey, status: { state: 'cleared' } }
      }, sessionKey);
    }
    return;
  }

  if (method === 'CancelTask' || method === 'tasks/cancel' || method === 'tasks_cancel') {
    const taskId = String(params.taskId || params.id || frame?.taskId || envelope?.taskId || '');
    const active = activeTasks.get(taskId);
    if (active) {
      active.cancelled = true;
      activeTasks.delete(taskId);
      if (active.machineId && active.sessionId) {
        await hubClient.interrupt(active.machineId, active.sessionId);
      }
    }
    emit({
      jsonrpc: '2.0',
      id,
      result: { id: taskId, status: { state: 'canceled' } }
    }, taskId);
    return;
  }

  const supportedSend = [
    'SendMessage',
    'SendStreamingMessage',
    'message/send',
    'message/stream',
    'tasks/send',
    'tasks/sendSubscribe'
  ];
  if (!supportedSend.includes(method)) {
    emit({ jsonrpc: '2.0', id, error: { code: -32601, message: `Unsupported method: ${method}` } });
    return;
  }

  const streaming = method === 'SendStreamingMessage' || method === 'message/stream' || method === 'tasks/sendSubscribe';
  const taskId = String(params.taskId || params.id || envelope?.taskId || randomUUID());
  const text = extractMessageText(params);
  if (!text) {
    emit({ jsonrpc: '2.0', id, error: { code: -32602, message: 'Empty message content' } }, taskId);
    return;
  }

  const stateHandle = { cancelled: false, machineId: '', sessionId: '' };
  activeTasks.set(taskId, stateHandle);

  if (streaming) {
    if (enveloped) {
      emit(formatEnvelopedStatusUpdate(id, taskId, 'working', '正在处理中，请稍候…'), taskId);
    } else {
      emit({ jsonrpc: '2.0', method: 'TaskStatusUpdate', params: { taskId, status: { state: 'working' } } }, taskId);
    }
  }

  try {
    const cmd = classifyRelayCommand(text);
    let replyText = '';
    let finalState = 'completed';

    if (cmd.kind === 'refuse_approval') {
      replyText = 'For your security, approvals cannot be confirmed over cloud relay. Open the Persona app on your unlocked phone to review and approve.';
      finalState = 'input-required';
    } else if (cmd.kind === 'start_recording') {
      const allowed = await hubClient.isRecordingAllowed();
      if (!allowed) {
        replyText = RECORDING_OFF_MESSAGE;
        finalState = 'rejected';
      } else {
        replyText = 'Open Persona on your phone to start Meeting Recording (microphone capture stays on-device).';
        finalState = 'input-required';
      }
    } else if (cmd.kind === 'status') {
      replyText = await hubClient.checkStatus();
    } else if (cmd.kind === 'add_todo') {
      replyText = await hubClient.addTodo(cmd.arg);
    } else if (cmd.kind === 'log_water') {
      replyText = await hubClient.logWater(cmd.amountMl);
    } else if (cmd.kind === 'send_agent') {
      replyText = await hubClient.sendAgentTask(cmd.arg, cmd.machineName || '');
    } else if (cmd.kind === 'search_kb') {
      replyText = await hubClient.searchKnowledge(cmd.arg);
    } else {
      const existing = sessionKey ? sessionContexts.get(sessionKey) : undefined;
      let chunkIndex = 0;
      const res = await hubClient.askPersona(cmd.arg, {
        conversationId: existing?.sessionId || '',
        machineId: existing?.machineId || '',
        isCancelled: () => stateHandle.cancelled,
        onTarget: (target) => {
          stateHandle.machineId = target.machineId;
          stateHandle.sessionId = target.sessionId;
        },
        onChunk: (delta, lastChunk) => {
          if (!streaming || stateHandle.cancelled || !delta) return;
          if (enveloped) {
            emit(
              formatEnvelopedArtifactUpdate(
                id,
                taskId,
                [{ kind: 'reasoningText', reasoningText: delta }],
                { append: chunkIndex > 0, lastChunk: true, final: false }
              ),
              taskId
            );
          } else {
            emit({
              jsonrpc: '2.0',
              method: 'TaskArtifactUpdate',
              params: {
                taskId,
                artifact: {
                  artifactId: `${taskId}:answer`,
                  parts: [{ kind: 'text', text: delta, mediaType: 'text/plain' }],
                  append: chunkIndex > 0,
                  lastChunk
                }
              }
            }, taskId);
          }
          chunkIndex++;
        }
      });
      if (res.canceled || stateHandle.cancelled) return;
      if (sessionKey && res.machineId && res.sessionId) {
        rememberSessionContext(sessionContexts, sessionKey, { machineId: res.machineId, sessionId: res.sessionId });
      }
      replyText = res.text;
    }

    if (enveloped && streaming) {
      const statusLabel = finalState === 'completed'
        ? '任务处理已完成~'
        : finalState === 'input-required'
          ? '请在 Persona 应用中确认'
          : '请求未能执行';
      emit(formatEnvelopedStatusUpdate(id, taskId, finalState, statusLabel), taskId);
      emit(
        formatEnvelopedArtifactUpdate(
          id,
          taskId,
          [{ kind: 'text', text: replyText }],
          { append: false, lastChunk: true, final: true }
        ),
        taskId
      );
    } else {
      emit({
        jsonrpc: '2.0',
        id,
        result: {
          id: taskId,
          status: {
            state: finalState,
            message: { role: 'agent', parts: [{ kind: 'text', text: replyText }] }
          },
          artifacts: [{ artifactId: `${taskId}:result`, parts: [{ kind: 'text', text: replyText }] }]
        }
      }, taskId);
    }
  } catch (error) {
    if (stateHandle.cancelled) return;
    const errMsg = error?.message || 'Hub request failed';
    if (enveloped && streaming) {
      emit(formatEnvelopedStatusUpdate(id, taskId, 'failed', '处理失败，请稍后重试'), taskId);
      emit(
        formatEnvelopedArtifactUpdate(
          id,
          taskId,
          [{ kind: 'text', text: errMsg }],
          { append: false, lastChunk: true, final: true }
        ),
        taskId
      );
    } else {
      emit({
        jsonrpc: '2.0',
        id,
        error: { code: -32000, message: errMsg }
      }, taskId);
    }
  } finally {
    if (activeTasks.get(taskId) === stateHandle) {
      activeTasks.delete(taskId);
    }
  }
}

export class OpenClawBridge {
  constructor(
    cfg,
    {
      WebSocketImpl = Rfc6455WebSocket,
      fetchImpl = globalThis.fetch,
      logger = console,
      stableConnectionMs = DEFAULT_STABLE_CONNECTION_MS
    } = {}
  ) {
    this.cfg = cfg;
    this.WebSocketImpl = WebSocketImpl;
    this.hub = new PersonaHubClient(cfg, fetchImpl);
    this.logger = logger;
    this.stableConnectionMs = Number.isFinite(stableConnectionMs) && stableConnectionMs >= 0
      ? stableConnectionMs
      : DEFAULT_STABLE_CONNECTION_MS;
    this.stopped = false;
    this.activeTasks = new Map();
    this.sessionContexts = new Map();
    this.sessionServerMap = new Map();
    this.servers = new Map();

    const endpoints = [{ id: 'server1', url: cfg.wsUrl }];
    if (cfg.wsUrl2) endpoints.push({ id: 'server2', url: cfg.wsUrl2 });
    for (const ep of endpoints) {
      this.servers.set(ep.id, {
        id: ep.id,
        url: ep.url,
        ws: null,
        attempt: 0,
        reconnectTimer: null,
        heartbeatTimer: null,
        stableTimer: null,
        lastPongAt: 0
      });
    }
  }

  get ws() {
    return this.servers.get('server1')?.ws || null;
  }

  start() {
    this.stopped = false;
    for (const serverState of this.servers.values()) {
      this.connectServer(serverState);
    }
  }

  stop() {
    this.stopped = true;
    for (const handle of this.activeTasks.values()) handle.cancelled = true;
    this.activeTasks.clear();
    for (const serverState of this.servers.values()) {
      if (serverState.reconnectTimer) {
        clearTimeout(serverState.reconnectTimer);
        serverState.reconnectTimer = null;
      }
      if (serverState.heartbeatTimer) {
        clearInterval(serverState.heartbeatTimer);
        serverState.heartbeatTimer = null;
      }
      if (serverState.stableTimer) {
        clearTimeout(serverState.stableTimer);
        serverState.stableTimer = null;
      }
      if (serverState.ws) {
        try { serverState.ws.close(1000, 'shutdown'); } catch (_) {}
        serverState.ws = null;
      }
    }
  }

  connectServer(serverState) {
    if (this.stopped) return;
    if (serverState.reconnectTimer) {
      clearTimeout(serverState.reconnectTimer);
      serverState.reconnectTimer = null;
    }

    const headers = signOpenClawHeaders(this.cfg);
    const wsUrl = new URL(serverState.url);
    if (this.cfg.authMode === 'hag' && !wsUrl.searchParams.has('agentId')) {
      wsUrl.searchParams.set('agentId', this.cfg.agentId);
    }

    const ws = new this.WebSocketImpl(wsUrl.toString(), { headers });
    serverState.ws = ws;

    ws.addEventListener('open', () => {
      if (serverState.ws !== ws) return;
      serverState.lastPongAt = Date.now();
      this.logger.info(`[xiaoyi-openclaw] [${serverState.id}] connected to ${wsUrl.host}`);
      if (serverState.stableTimer) clearTimeout(serverState.stableTimer);
      serverState.stableTimer = setTimeout(() => {
        if (serverState.ws === ws && !this.stopped) {
          serverState.attempt = 0;
        }
      }, this.stableConnectionMs);
      if (typeof serverState.stableTimer.unref === 'function') serverState.stableTimer.unref();

      ws.send(JSON.stringify({ msgType: 'clawd_bot_init', agentId: this.cfg.agentId }));
      if (serverState.heartbeatTimer) clearInterval(serverState.heartbeatTimer);
      serverState.heartbeatTimer = setInterval(() => {
        if (serverState.ws === ws && ws.readyState === 1) {
          ws.send(JSON.stringify({ msgType: 'heartbeat', agentId: this.cfg.agentId, timestamp: Date.now() }));
          if (typeof ws.ping === 'function') ws.ping();
        }
      }, this.cfg.heartbeatMs);
    });

    ws.addEventListener('pong', () => {
      if (serverState.ws === ws) serverState.lastPongAt = Date.now();
    });

    ws.addEventListener('message', (event) => {
      if (serverState.ws !== ws) return;
      let rawFrame;
      try {
        rawFrame = JSON.parse(String(event.data || ''));
      } catch (_) {
        return;
      }
      if (typeof rawFrame?.agentId === 'string' && rawFrame.agentId && rawFrame.agentId !== this.cfg.agentId) {
        this.logger.warn(`[xiaoyi-openclaw] [${serverState.id}] mismatched agentId=${rawFrame.agentId}, discarding`);
        return;
      }
      const unwrapped = unwrapOpenClawEnvelope(rawFrame, this.cfg.agentId);
      if (unwrapped.ignore) return;

      const sessionId = unwrapped.envelope?.sessionId || unwrapped.rpcFrame?.params?.sessionId || unwrapped.rpcFrame?.sessionId || '';
      if (sessionId) {
        this.sessionServerMap.set(sessionId, serverState.id);
      }

      const rpcMethod = unwrapped.rpcFrame?.method || unwrapped.rpcFrame?.action || 'unknown';
      const rpcTask = unwrapped.envelope?.taskId || unwrapped.rpcFrame?.params?.id || unwrapped.rpcFrame?.id || '';
      this.logger.info(`[xiaoyi-openclaw] [${serverState.id}] rpc method=${rpcMethod} session=${sessionId} task=${rpcTask}`);

      const sendReply = (reply) => {
        if (reply?.msgType === 'agent_response') {
          let detailKind = '';
          let detailFinal = '';
          try {
            const parsedDetail = JSON.parse(reply.msgDetail || '{}');
            detailKind = parsedDetail?.result?.kind || (parsedDetail?.error ? 'error' : 'rpc');
            detailFinal = String(Boolean(parsedDetail?.result?.final));
          } catch (_) {}
          this.logger.info(
            `[xiaoyi-openclaw] [${serverState.id}] sent msgType=agent_response task=${reply.taskId || ''} kind=${detailKind} final=${detailFinal}`
          );
        }
        if (serverState.ws === ws && ws.readyState === 1) {
          ws.send(JSON.stringify(reply));
          return;
        }
        const mappedId = sessionId ? this.sessionServerMap.get(sessionId) : '';
        const target = mappedId ? this.servers.get(mappedId) : null;
        if (target?.ws && target.ws.readyState === 1) {
          target.ws.send(JSON.stringify(reply));
        }
      };

      handleOpenClawRpc(
        unwrapped.rpcFrame,
        this.hub,
        sendReply,
        this.activeTasks,
        {
          enveloped: unwrapped.enveloped,
          envelope: unwrapped.envelope,
          sessionContexts: this.sessionContexts
        }
      ).catch((err) => {
        this.logger.error(`[xiaoyi-openclaw] [${serverState.id}] rpc error:`, err?.message || err);
      });
    });

    ws.addEventListener('close', (ev) => {
      if (serverState.ws !== ws) return;
      serverState.ws = null;
      if (serverState.heartbeatTimer) {
        clearInterval(serverState.heartbeatTimer);
        serverState.heartbeatTimer = null;
      }
      if (serverState.stableTimer) {
        clearTimeout(serverState.stableTimer);
        serverState.stableTimer = null;
      }
      if (this.stopped) return;
      const delay = computeReconnectDelayMs(serverState.attempt++);
      const reason = ev?.reason ? ` (${ev.reason})` : '';
      this.logger.warn(`[xiaoyi-openclaw] [${serverState.id}] disconnected (code=${ev?.code || 0}${reason}), reconnecting in ${delay}ms`);
      serverState.reconnectTimer = setTimeout(() => this.connectServer(serverState), delay);
    });

    ws.addEventListener('error', (err) => {
      this.logger.error(`[xiaoyi-openclaw] [${serverState.id}] socket error:`, err?.message || 'connection error');
    });
  }
}

async function probeWsEndpoint(cfg, wsUrlStr, { WebSocketImpl, wsTimeoutMs, wsStayOpenMs }) {
  return new Promise((resolve) => {
    let settled = false;
    let stayTimer = null;
    const done = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeoutTimer);
      if (stayTimer) clearTimeout(stayTimer);
      resolve(result);
    };
    const timeoutTimer = setTimeout(() => {
      try { ws?.close(); } catch (_) {}
      done({ ok: false, detail: `timed out after ${wsTimeoutMs}ms` });
    }, wsTimeoutMs);

    let ws;
    try {
      const headers = signOpenClawHeaders(cfg);
      const wsUrl = new URL(wsUrlStr);
      if (cfg.authMode === 'hag' && !wsUrl.searchParams.has('agentId')) {
        wsUrl.searchParams.set('agentId', cfg.agentId);
      }
      ws = new WebSocketImpl(wsUrl.toString(), { headers });
    } catch (err) {
      done({ ok: false, detail: err?.message || 'invalid WebSocket config' });
      return;
    }

    let opened = false;
    ws.addEventListener('open', () => {
      opened = true;
      try {
        ws.send(JSON.stringify({ msgType: 'clawd_bot_init', agentId: cfg.agentId }));
      } catch (err) {
        done({ ok: false, detail: err?.message || 'failed to send clawd_bot_init' });
        return;
      }
      stayTimer = setTimeout(() => {
        try { ws.close(1000, 'preflight-ok'); } catch (_) {}
        done({ ok: true, detail: `${new URL(wsUrlStr).host} stayed open ${wsStayOpenMs}ms after clawd_bot_init` });
      }, wsStayOpenMs);
    });
    ws.addEventListener('error', (err) => {
      if (!opened) {
        done({ ok: false, detail: err?.message || 'WebSocket connection failed' });
      }
    });
    ws.addEventListener('close', (ev) => {
      if (!settled) {
        const reason = ev?.reason ? ` (${ev.reason})` : '';
        const phase = opened ? 'closed after clawd_bot_init' : 'closed before open';
        done({ ok: false, detail: `${new URL(wsUrlStr).host} ${phase} (code ${ev?.code || 0}${reason})` });
      }
    });
  });
}

/**
 * Preflight check (`--check` / `--check-ws`) so a developer can validate Hub reachability, token auth,
 * recording module state, and optional live OpenClaw WebSocket handshake without printing secrets.
 */
export async function runPreflightCheck(
  cfg,
  { checkWs = false, fetchImpl = globalThis.fetch, WebSocketImpl = Rfc6455WebSocket, wsTimeoutMs = 8000, wsStayOpenMs = 1500 } = {}
) {
  const hub = new PersonaHubClient(cfg, fetchImpl);
  const report = {
    ok: false,
    hubUrl: cfg.hubUrl,
    wsUrl: cfg.wsUrl,
    wsUrl2: cfg.wsUrl2 || '',
    authMode: cfg.authMode,
    agentId: cfg.agentId,
    checks: {
      config: { ok: true, detail: `authMode=${cfg.authMode}${cfg.wsUrl2 ? ', dual-server=on' : ''}` },
      hubHealth: { ok: false, detail: '' },
      hubAuth: { ok: false, detail: '' },
      assistant: { ok: false, detail: '' },
      recordingModule: { ok: true, allowed: true, detail: 'on' },
      wsSigning: { ok: false, detail: '' },
      wsHandshake: checkWs ? { ok: false, detail: '' } : { ok: true, skipped: true, detail: 'skipped (pass --check-ws to test live socket)' }
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
      detail: err?.status === 401 || err?.status === 403 ? 'token rejected (HTTP 401/403)' : (err?.message || 'failed')
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

  try {
    const headers = signOpenClawHeaders(cfg);
    const headerNames = Object.keys(headers).join(', ');
    report.checks.wsSigning = { ok: true, detail: `signed headers ready (${headerNames})` };
  } catch (err) {
    report.checks.wsSigning = { ok: false, detail: err?.message || 'signing failed' };
  }

  if (checkWs) {
    const urls = [cfg.wsUrl];
    if (cfg.wsUrl2) urls.push(cfg.wsUrl2);
    const results = await Promise.all(
      urls.map((u) => probeWsEndpoint(cfg, u, { WebSocketImpl, wsTimeoutMs, wsStayOpenMs }))
    );
    const anyOk = results.some((r) => r.ok);
    report.checks.wsHandshake = {
      ok: anyOk,
      detail: results.map((r, idx) => `server${idx + 1} [${r.ok ? 'PASS' : 'FAIL'}]: ${r.detail}`).join('; ')
    };
  }

  report.ok = Object.values(report.checks).every((c) => c.ok);
  return report;
}

export function formatPreflightReport(report) {
  const lines = [
    `Persona XiaoYi OpenClaw Preflight (${report.ok ? 'PASS' : 'FAIL'})`,
    `  Hub: ${report.hubUrl}`,
    `  WS1: ${report.wsUrl} (agentId=${report.agentId}, authMode=${report.authMode})`
  ];
  if (report.wsUrl2) {
    lines.push(`  WS2: ${report.wsUrl2}`);
  }
  for (const [name, entry] of Object.entries(report.checks)) {
    const badge = entry.skipped ? 'SKIP' : entry.ok ? 'PASS' : 'FAIL';
    lines.push(`  [${badge}] ${name}: ${entry.detail}`);
  }
  return lines.join('\n');
}

function printUsage() {
  console.log([
    'Persona XiaoYi OpenClaw Outbound WebSocket Bridge (Node 24, zero dependencies)',
    '',
    'Usage:',
    '  node harmony/scripts/xiaoyi-openclaw-bridge.mjs [--config /path/to/config.json]',
    '  node harmony/scripts/xiaoyi-openclaw-bridge.mjs --check [--check-ws] [--config /path/to/config.json]',
    '',
    'Required env vars (or JSON keys):',
    '  XIAOYI_AK / ak, XIAOYI_SK / sk, XIAOYI_AGENT_ID / agentId,',
    '  PERSONA_HUB_URL / hubUrl, PERSONA_HUB_TOKEN / hubToken',
    '',
    'Optional env vars:',
    `  XIAOYI_WS_URL (default: ${DEFAULT_OPENCLAW_WS_URL})`,
    `  XIAOYI_WS_URL_2 (default: ${DEFAULT_OPENCLAW_WS_URL_2} when WS1 uses default; "none" to disable)`,
    '  XIAOYI_AUTH_MODE ("huawei" default or "hag"), PERSONA_MANAGER, XIAOYI_CONFIG_FILE'
  ].join('\n'));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const cli = parseCliArgs(process.argv.slice(2));
  if (cli.help) {
    printUsage();
    process.exit(0);
  }
  const cfg = loadBridgeConfig(process.env, cli.configFile);
  if (cli.check) {
    const report = await runPreflightCheck(cfg, { checkWs: cli.checkWs });
    console.log(formatPreflightReport(report));
    process.exit(report.ok ? 0 : 1);
  }
  const bridge = new OpenClawBridge(cfg);
  process.on('SIGINT', () => { bridge.stop(); process.exit(0); });
  process.on('SIGTERM', () => { bridge.stop(); process.exit(0); });
  bridge.start();
}
