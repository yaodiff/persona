import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  DEFAULT_CLOUD_HOST,
  DEFAULT_CLOUD_PORT,
  MAX_BODY_BYTES,
  MIN_CLOUD_TOKEN_LENGTH,
  buildAgentCard,
  createXiaoYiCloudServer,
  formatCloudPreflightReport,
  loadCloudServerConfig,
  parseCloudCliArgs,
  runCloudPreflightCheck,
  verifyBearerToken
} from '../scripts/xiaoyi-cloud-server.mjs';
import { RECORDING_OFF_MESSAGE } from '../scripts/xiaoyi-openclaw-bridge.mjs';

const VALID_CLOUD_TOKEN = 'cloud-token-with-sufficient-entropy-0123456789';

function createMockHubServer({ recordingEnabled = true, rejectAuth = false, waterEnabled = true, slowAskMs = 0 } = {}) {
  const requests = [];
  let messageSent = false;
  let holdWorking = false;
  let waterToday = 500;
  let pollCount = 0;

  const server = http.createServer((req, res) => {
    let body = '';
    req.on('data', (chunk) => {
      body += chunk;
    });
    req.on('end', async () => {
      requests.push({
        method: req.method,
        url: req.url,
        auth: req.headers.authorization,
        client: req.headers['x-persona-client'] || '',
        source: req.headers['x-persona-source'] || '',
        manager: req.headers['x-persona-manager'] || '',
        body
      });
      res.setHeader('Content-Type', 'application/json');
      if (rejectAuth && req.url !== '/api/v1/health') {
        res.statusCode = 401;
        res.end(JSON.stringify({ error: 'unauthorized' }));
        return;
      }
      if (req.url === '/api/v1/health') {
        res.end(
          JSON.stringify({
            ok: true,
            hub_id: 'hub-cloud-test',
            hub_name: 'Studio Hub',
            version: '1.0.0',
            features: recordingEnabled ? ['modules', 'module.recording', 'module.voice'] : ['modules', 'module.voice']
          })
        );
      } else if (req.url === '/api/v1/machines') {
        res.end(
          JSON.stringify({
            machines: [
              { id: 'm1', name: 'Studio', online: true, trust: 'trusted' },
              { id: 'm2', name: 'Laptop', online: false, trust: 'trusted' }
            ]
          })
        );
      } else if (req.url === '/api/v1/sessions') {
        const isWorking = holdWorking;
        if (holdWorking && pollCount >= 1) {
          holdWorking = false;
        }
        res.end(
          JSON.stringify({
            sessions: [
              {
                id: 'main-1',
                machine_id: 'm1',
                assistant: true,
                state: isWorking ? 'working' : 'idle',
                status: isWorking ? 'working' : 'idle'
              },
              {
                id: 's1',
                machine_id: 'm1',
                title: 'RepoWorker',
                assistant: false,
                state: 'working',
                status: 'working',
                updated_at: '2026-11-27T08:00:00Z'
              }
            ]
          })
        );
      } else if (req.url === '/api/v1/interactions') {
        res.end(
          JSON.stringify({
            interactions: [{ id: 'dec-1', kind: 'permission', status: 'open', summary: 'Run deploy script' }]
          })
        );
      } else if (req.url === '/api/v1/todos' && req.method === 'POST') {
        const parsed = JSON.parse(body);
        res.end(JSON.stringify({ todo: { id: 'todo-9', title: parsed.title } }));
      } else if (req.url === '/api/v1/assistant/health/intake?space=health' && req.method === 'GET') {
        res.end(
          JSON.stringify({
            items: waterEnabled ? ['water', 'protein'] : ['protein'],
            water: { today: waterToday, goal: 2000 }
          })
        );
      } else if (req.url === '/api/v1/assistant/health/intake?space=health' && req.method === 'POST') {
        const parsed = JSON.parse(body);
        const amt = Number(parsed.amount || 250);
        waterToday += amt;
        res.end(
          JSON.stringify({
            entry: { id: parsed.id, kind: 'water', amount: amt },
            today: { water: waterToday, protein: 0 },
            zone: 'Asia/Shanghai'
          })
        );
      } else if (req.url.startsWith('/api/v1/kb-search')) {
        res.end(JSON.stringify({ hits: [{ path: 'docs/harmony/xiaoyi.md', line: 'Cloud A2A v1.0 & MCP' }] }));
      } else if (req.url === '/api/v1/assistant') {
        res.end(JSON.stringify({ main: 'main-1', main_machine_id: 'm1', conversations: [] }));
      } else if (req.url.includes('/interrupt') && req.method === 'POST') {
        holdWorking = false;
        res.end(JSON.stringify({ ok: true }));
      } else if (req.url.includes('/main-1/messages') && req.method === 'POST') {
        messageSent = true;
        pollCount = 0;
        res.end(JSON.stringify({ ok: true }));
      } else if (req.url.includes('/messages') && req.method === 'POST') {
        res.end(JSON.stringify({ ok: true }));
      } else if (req.url.includes('/items')) {
        if (slowAskMs > 0) {
          await new Promise((r) => setTimeout(r, slowAskMs));
        }
        if (!messageSent) {
          res.end(JSON.stringify({ items: [{ seq: 1, kind: 'text', role: 'assistant', text: 'Previous turn' }] }));
          return;
        }
        pollCount++;
        if (pollCount === 1 && holdWorking) {
          res.end(JSON.stringify({ items: [{ seq: 2, kind: 'text', role: 'assistant', text: 'First chunk ' }] }));
          return;
        }
        res.end(
          JSON.stringify({
            items: [{ seq: 2, kind: 'text', role: 'assistant', text: 'First chunk plus final reply.' }]
          })
        );
      } else {
        res.statusCode = 404;
        res.end(JSON.stringify({ error: 'not found' }));
      }
    });
  });

  return {
    server,
    requests,
    setHoldWorking(v) {
      holdWorking = v;
    },
    resetMessageSent() {
      messageSent = false;
      pollCount = 0;
    },
    async listen() {
      await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
      return `http://127.0.0.1:${server.address().port}`;
    },
    async close() {
      await new Promise((resolve) => server.close(resolve));
    }
  };
}

function parseSseEvents(rawText) {
  return rawText
    .split(/\r?\n\r?\n/)
    .map((block) => block.trim())
    .filter(Boolean)
    .map((block) => {
      const dataLines = block
        .split(/\r?\n/)
        .filter((line) => line.startsWith('data:'))
        .map((line) => line.slice('data:'.length).trim());
      return JSON.parse(dataLines.join('\n'));
    });
}

test('parseCloudCliArgs, loadCloudServerConfig, and verifyBearerToken enforce fail-closed validation', () => {
  assert.deepEqual(parseCloudCliArgs(['--check', '--config', '/tmp/cloud.json']), {
    check: true,
    help: false,
    configFile: '/tmp/cloud.json'
  });
  assert.deepEqual(parseCloudCliArgs(['--config=/tmp/alt.json', '-h']), {
    check: false,
    help: true,
    configFile: '/tmp/alt.json'
  });

  assert.throws(() => loadCloudServerConfig({}), /Missing required configuration/);
  assert.throws(
    () =>
      loadCloudServerConfig({
        XIAOYI_CLOUD_TOKEN: 'too-short-token',
        PERSONA_HUB_URL: 'https://hub.example.com',
        PERSONA_HUB_TOKEN: 'hub-tok'
      }),
    new RegExp(`at least ${MIN_CLOUD_TOKEN_LENGTH}`)
  );
  assert.throws(
    () =>
      loadCloudServerConfig({
        XIAOYI_CLOUD_TOKEN: VALID_CLOUD_TOKEN,
        XIAOYI_CLOUD_PORT: '70000',
        PERSONA_HUB_URL: 'https://hub.example.com',
        PERSONA_HUB_TOKEN: 'hub-tok'
      }),
    /between 0 and 65535/
  );
  assert.throws(
    () =>
      loadCloudServerConfig({
        XIAOYI_CLOUD_TOKEN: VALID_CLOUD_TOKEN,
        PERSONA_HUB_URL: 'ftp://hub.example.com',
        PERSONA_HUB_TOKEN: 'hub-tok'
      }),
    /http:\/\/ or https:\/\//
  );
  assert.throws(
    () =>
      loadCloudServerConfig({
        XIAOYI_CLOUD_TOKEN: VALID_CLOUD_TOKEN,
        XIAOYI_PUBLIC_BASE_URL: 'ws://bad.example.com',
        PERSONA_HUB_URL: 'https://hub.example.com',
        PERSONA_HUB_TOKEN: 'hub-tok'
      }),
    /XIAOYI_PUBLIC_BASE_URL/
  );

  const defaultCfg = loadCloudServerConfig({
    XIAOYI_CLOUD_TOKEN: VALID_CLOUD_TOKEN,
    PERSONA_HUB_URL: 'https://hub.example.com/',
    PERSONA_HUB_TOKEN: 'hub-tok'
  });
  assert.equal(defaultCfg.host, DEFAULT_CLOUD_HOST);
  assert.equal(defaultCfg.port, DEFAULT_CLOUD_PORT);
  assert.equal(defaultCfg.hubUrl, 'https://hub.example.com');
  assert.equal(defaultCfg.publicBaseUrl, '');

  const dir = mkdtempSync(join(tmpdir(), 'xiaoyi-cloud-cfg-'));
  try {
    const cfgPath = join(dir, 'cloud.json');
    writeFileSync(
      cfgPath,
      JSON.stringify({
        cloudToken: VALID_CLOUD_TOKEN,
        host: '0.0.0.0',
        port: 0,
        publicBaseUrl: 'https://cloud.example.com/',
        hubUrl: 'https://hub.example.com/',
        hubToken: 'hub-secret',
        manager: 'work'
      }),
      { mode: 0o600 }
    );
    const fileCfg = loadCloudServerConfig({}, cfgPath);
    assert.equal(fileCfg.host, '0.0.0.0');
    assert.equal(fileCfg.port, 0);
    assert.equal(fileCfg.publicBaseUrl, 'https://cloud.example.com');
    assert.equal(fileCfg.manager, 'work');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }

  assert.equal(verifyBearerToken(`Bearer ${VALID_CLOUD_TOKEN}`, VALID_CLOUD_TOKEN), true);
  assert.equal(verifyBearerToken(`bearer   ${VALID_CLOUD_TOKEN}`, VALID_CLOUD_TOKEN), true);
  assert.equal(verifyBearerToken(`Bearer ${VALID_CLOUD_TOKEN}x`, VALID_CLOUD_TOKEN), false);
  assert.equal(verifyBearerToken('Bearer short', VALID_CLOUD_TOKEN), false);
  assert.equal(verifyBearerToken('Basic abc123', VALID_CLOUD_TOKEN), false);
  assert.equal(verifyBearerToken('Bearer    ', VALID_CLOUD_TOKEN), false);
  assert.equal(verifyBearerToken('', VALID_CLOUD_TOKEN), false);
  assert.equal(verifyBearerToken(`Bearer ${VALID_CLOUD_TOKEN}`, ''), false);
});

test('healthz, agent-card.json (fallback & publicBaseUrl), 401 fail-closed auth, and 413 body limit', async () => {
  const hub = createMockHubServer();
  const hubUrl = await hub.listen();

  const cloudFallback = createXiaoYiCloudServer({
    cloudToken: VALID_CLOUD_TOKEN,
    host: '127.0.0.1',
    port: 0,
    hubUrl,
    hubToken: 'hub-secret'
  });
  const { origin } = await cloudFallback.listen();

  try {
    const healthRes = await fetch(`${origin}/healthz`);
    assert.equal(healthRes.status, 200);
    assert.deepEqual(await healthRes.json(), { ok: true, service: 'persona-xiaoyi-cloud' });

    const cardRes = await fetch(`${origin}/.well-known/agent-card.json`);
    assert.equal(cardRes.status, 200);
    const card = await cardRes.json();
    assert.equal(card.name, 'Persona');
    assert.equal(card.version, '1.0.0');
    assert.equal(card.protocolVersion, '1.0');
    assert.equal(card.url, `${origin}/a2a`);
    assert.equal(card.interfaces[0].url, `${origin}/a2a`);
    assert.deepEqual(card.capabilities, {
      streaming: true,
      pushNotifications: false,
      stateTransitionHistory: true
    });
    assert.deepEqual(card.defaultInputModes, ['text/plain', 'application/json']);
    assert.deepEqual(card.defaultOutputModes, ['text/plain', 'application/json']);
    assert.deepEqual(card.securitySchemes, { bearer: { type: 'http', scheme: 'bearer' } });
    assert.deepEqual(card.security, [{ bearer: [] }]);
    assert.deepEqual(
      card.skills.map((s) => s.id),
      ['ask_persona', 'check_status', 'send_agent_task', 'add_todo', 'log_water', 'search_knowledge']
    );

    const publicCard = buildAgentCard(
      {
        cloudToken: VALID_CLOUD_TOKEN,
        host: '127.0.0.1',
        port: 18790,
        publicBaseUrl: 'https://persona.example.com'
      },
      origin
    );
    assert.equal(publicCard.url, 'https://persona.example.com/a2a');
    assert.equal(publicCard.interfaces[0].url, 'https://persona.example.com/a2a');

    for (const endpoint of ['/a2a', '/mcp']) {
      const noAuth = await fetch(`${origin}${endpoint}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'ping' })
      });
      assert.equal(noAuth.status, 401);
      assert.equal(noAuth.headers.get('www-authenticate'), 'Bearer');
      const noAuthJson = await noAuth.json();
      assert.equal(noAuthJson.error.code, -32001);

      const badAuth = await fetch(`${origin}${endpoint}`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: 'Bearer wrong-token-value-1234567890'
        },
        body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'ping' })
      });
      assert.equal(badAuth.status, 401);
      assert.equal(badAuth.headers.get('www-authenticate'), 'Bearer');
    }

    const oversized = JSON.stringify({
      jsonrpc: '2.0',
      id: 'big',
      method: 'message/send',
      params: { message: { parts: [{ kind: 'text', text: 'x'.repeat(MAX_BODY_BYTES + 1024) }] } }
    });
    const tooBigRes = await fetch(`${origin}/a2a`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${VALID_CLOUD_TOKEN}`
      },
      body: oversized
    });
    assert.equal(tooBigRes.status, 413);
    const tooBigJson = await tooBigRes.json();
    assert.equal(tooBigJson.error.message, 'Payload Too Large');
  } finally {
    await cloudFallback.close();
    await hub.close();
  }
});

test('POST /a2a handles commands, approval refusal, #15985 recording guard, SSE streaming, tasks/get, and tasks/cancel', async () => {
  const hub = createMockHubServer({ recordingEnabled: false });
  const hubUrl = await hub.listen();
  const cloud = createXiaoYiCloudServer({
    cloudToken: VALID_CLOUD_TOKEN,
    host: '127.0.0.1',
    port: 0,
    hubUrl,
    hubToken: 'hub-secret',
    manager: 'work',
    askTimeoutMs: 4000
  });
  const { origin } = await cloud.listen();
  const authHeaders = {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${VALID_CLOUD_TOKEN}`
  };

  const rpc = async (payload) => {
    const res = await fetch(`${origin}/a2a`, {
      method: 'POST',
      headers: authHeaders,
      body: JSON.stringify(payload)
    });
    return { status: res.status, json: await res.json() };
  };

  try {
    // Malformed JSON -> 400 -32700
    const badJsonRes = await fetch(`${origin}/a2a`, {
      method: 'POST',
      headers: authHeaders,
      body: '{not-json'
    });
    assert.equal(badJsonRes.status, 400);
    assert.equal((await badJsonRes.json()).error.code, -32700);

    // Unknown method -> -32601
    const unknownRes = await rpc({ jsonrpc: '2.0', id: 'u1', method: 'unknown/method', params: {} });
    assert.equal(unknownRes.json.error.code, -32601);

    // Empty message -> -32602
    const emptyRes = await rpc({
      jsonrpc: '2.0',
      id: 'e1',
      method: 'message/send',
      params: { message: { parts: [{ kind: 'text', text: '   ' }] } }
    });
    assert.equal(emptyRes.json.error.code, -32602);

    // Approval refusal -> input-required, never calls Hub /resolve
    const approvalRes = await rpc({
      jsonrpc: '2.0',
      id: 'ap1',
      method: 'message/send',
      params: { id: 'task-ap-1', message: { parts: [{ kind: 'text', text: 'Approve pending' }] } }
    });
    assert.equal(approvalRes.json.result.status.state, 'input-required');
    assert.ok(approvalRes.json.result.status.message.parts[0].text.includes('approval_requires_phone'));
    assert.equal(hub.requests.some((r) => r.url.includes('/resolve')), false);

    // #15985 recording off -> rejected + RECORDING_OFF_MESSAGE
    const recRes = await rpc({
      jsonrpc: '2.0',
      id: 'rec1',
      method: 'message/send',
      params: { id: 'task-rec-1', message: { parts: [{ kind: 'text', text: 'Start meeting recording' }] } }
    });
    assert.equal(recRes.json.result.status.state, 'rejected');
    assert.equal(recRes.json.result.status.message.parts[0].text, RECORDING_OFF_MESSAGE);

    // Status, todo, water, agent task, kb search
    const statusRes = await rpc({
      jsonrpc: '2.0',
      id: 'st1',
      method: 'message/send',
      params: { id: 'task-st-1', message: { parts: [{ kind: 'text', text: 'Check status' }] } }
    });
    assert.ok(statusRes.json.result.status.message.parts[0].text.includes('1 working, 1 need you'));
    const hubReq = hub.requests.find((r) => r.url === '/api/v1/sessions');
    assert.equal(hubReq.client, 'harmony-xiaoyi-cloud/1.0.0');
    assert.equal(hubReq.source, 'xiaoyi-cloud');

    const todoRes = await rpc({
      jsonrpc: '2.0',
      id: 'td1',
      method: 'SendMessage',
      params: { message: { parts: [{ kind: 'text', text: 'Add a to-do: verify cloud server' }] } }
    });
    assert.equal(todoRes.json.result.status.message.parts[0].text, 'To-do saved: verify cloud server');

    const waterRes = await rpc({
      jsonrpc: '2.0',
      id: 'wt1',
      method: 'tasks/send',
      params: { message: { parts: [{ kind: 'text', text: 'Log water 350 ml' }] } }
    });
    assert.equal(waterRes.json.result.status.message.parts[0].text, 'Logged 350 ml. Today: 850 / 2000 ml.');

    const agentRes = await rpc({
      jsonrpc: '2.0',
      id: 'ag1',
      method: 'message/send',
      params: { message: { parts: [{ kind: 'text', text: 'Send to agent: on Studio: run tests' }] } }
    });
    assert.equal(agentRes.json.result.status.message.parts[0].text, 'Sent to RepoWorker (Studio).');

    const kbRes = await rpc({
      jsonrpc: '2.0',
      id: 'kb1',
      method: 'message/send',
      params: { message: { parts: [{ kind: 'text', text: 'Search knowledge: A2A' }] } }
    });
    assert.ok(kbRes.json.result.status.message.parts[0].text.includes('docs/harmony/xiaoyi.md'));

    // tasks/get lookup + missing
    const getRes = await rpc({ jsonrpc: '2.0', id: 'g1', method: 'tasks/get', params: { id: 'task-st-1' } });
    assert.equal(getRes.json.result.id, 'task-st-1');
    assert.equal(getRes.json.result.status.state, 'completed');

    const missRes = await rpc({ jsonrpc: '2.0', id: 'g2', method: 'tasks/get', params: { id: 'does-not-exist' } });
    assert.equal(missRes.json.error.code, -32001);

    // message/stream SSE with multi-chunk streaming
    hub.setHoldWorking(true);
    const streamRes = await fetch(`${origin}/a2a`, {
      method: 'POST',
      headers: authHeaders,
      body: JSON.stringify({
        jsonrpc: '2.0',
        id: 'str-1',
        method: 'message/stream',
        params: {
          id: 'task-stream-1',
          contextId: 'ctx-stream-1',
          message: { parts: [{ kind: 'text', text: 'Summarize my day' }] }
        }
      })
    });
    assert.equal(streamRes.status, 200);
    assert.ok(streamRes.headers.get('content-type').startsWith('text/event-stream'));
    assert.equal(streamRes.headers.get('cache-control'), 'no-cache');
    const sseText = await streamRes.text();
    const events = parseSseEvents(sseText);
    assert.ok(events.some((e) => e.result?.kind === 'status-update' && e.result?.status?.state === 'working'));
    const chunkUpdates = events.filter((e) => e.result?.kind === 'artifact-update' && e.result?.final === false);
    assert.ok(chunkUpdates.length >= 2);
    assert.equal(chunkUpdates[0].result.append, false);
    assert.ok(chunkUpdates.some((e) => e.result.append === true));
    assert.ok(chunkUpdates.some((e) => e.result.lastChunk === true));
    const finalArtifact = events.find((e) => e.result?.kind === 'artifact-update' && e.result?.final === true);
    assert.equal(finalArtifact.result.artifact.parts[0].text, 'First chunk plus final reply.');
    const finalStatus = events.find((e) => e.result?.kind === 'status-update' && e.result?.final === true);
    assert.equal(finalStatus.result.status.state, 'completed');

    // tasks/cancel interrupts in-flight Hub session
    hub.resetMessageSent();
    hub.setHoldWorking(true);
    const inFlightPromise = fetch(`${origin}/a2a`, {
      method: 'POST',
      headers: authHeaders,
      body: JSON.stringify({
        jsonrpc: '2.0',
        id: 'c-run',
        method: 'message/send',
        params: {
          id: 'task-cancel-1',
          contextId: 'ctx-cancel-1',
          message: { parts: [{ kind: 'text', text: 'Long running question' }] }
        }
      })
    });
    // Wait briefly until task has bound machineId/sessionId via /api/v1/assistant + baseline read
    const deadline = Date.now() + 2000;
    while (Date.now() < deadline) {
      const t = cloud.taskStore.get('task-cancel-1');
      if (t && t.machineId && t.sessionId) break;
      await new Promise((r) => setTimeout(r, 20));
    }
    const cancelRes = await rpc({
      jsonrpc: '2.0',
      id: 'c-1',
      method: 'tasks/cancel',
      params: { id: 'task-cancel-1' }
    });
    assert.equal(cancelRes.json.result.id, 'task-cancel-1');
    assert.equal(cancelRes.json.result.status.state, 'canceled');
    const inFlightRes = await inFlightPromise;
    const inFlightJson = await inFlightRes.json();
    assert.equal(inFlightJson.result.status.state, 'canceled');
    assert.ok(hub.requests.some((r) => r.url.includes('/interrupt') && r.method === 'POST'));
  } finally {
    await cloud.close();
    await hub.close();
  }
});

test('POST /mcp handles initialize, notifications/initialized, tools/list, and tools/call across all 6 tools', async () => {
  const hub = createMockHubServer({ recordingEnabled: false });
  const hubUrl = await hub.listen();
  const cloud = createXiaoYiCloudServer({
    cloudToken: VALID_CLOUD_TOKEN,
    host: '127.0.0.1',
    port: 0,
    hubUrl,
    hubToken: 'hub-secret',
    askTimeoutMs: 4000
  });
  const { origin } = await cloud.listen();
  const authHeaders = {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${VALID_CLOUD_TOKEN}`
  };

  const mcp = async (payload) => {
    const res = await fetch(`${origin}/mcp`, {
      method: 'POST',
      headers: authHeaders,
      body: JSON.stringify(payload)
    });
    const text = await res.text();
    return { status: res.status, text, json: text ? JSON.parse(text) : null };
  };

  try {
    const initRes = await mcp({ jsonrpc: '2.0', id: 1, method: 'initialize', params: {} });
    assert.equal(initRes.status, 200);
    assert.equal(initRes.json.result.protocolVersion, '2025-03-26');
    assert.deepEqual(initRes.json.result.capabilities, { tools: { listChanged: false } });
    assert.deepEqual(initRes.json.result.serverInfo, { name: 'persona-xiaoyi-cloud', version: '1.0.0' });

    const notifRes = await mcp({ jsonrpc: '2.0', method: 'notifications/initialized' });
    assert.equal(notifRes.status, 202);
    assert.equal(notifRes.text, '');

    const pingRes = await mcp({ jsonrpc: '2.0', id: 2, method: 'ping' });
    assert.deepEqual(pingRes.json.result, {});

    const listRes = await mcp({ jsonrpc: '2.0', id: 3, method: 'tools/list' });
    assert.equal(listRes.json.result.tools.length, 6);
    assert.deepEqual(
      listRes.json.result.tools.map((t) => t.name),
      ['ask_persona', 'check_status', 'send_agent_task', 'add_todo', 'log_water', 'search_knowledge']
    );
    for (const tool of listRes.json.result.tools) {
      assert.equal(tool.inputSchema.type, 'object');
    }

    const askRes = await mcp({
      jsonrpc: '2.0',
      id: 4,
      method: 'tools/call',
      params: { name: 'ask_persona', arguments: { question: 'Summarize my notes' } }
    });
    assert.equal(askRes.json.result.isError, false);
    assert.equal(askRes.json.result.content[0].text, 'First chunk plus final reply.');

    const statusRes = await mcp({
      jsonrpc: '2.0',
      id: 5,
      method: 'tools/call',
      params: { name: 'check_status', arguments: {} }
    });
    assert.equal(statusRes.json.result.isError, false);
    assert.ok(statusRes.json.result.content[0].text.includes('1 working, 1 need you'));

    const agentRes = await mcp({
      jsonrpc: '2.0',
      id: 6,
      method: 'tools/call',
      params: { name: 'send_agent_task', arguments: { prompt: 'run lint', machine: 'Studio', session: 'RepoWorker' } }
    });
    assert.equal(agentRes.json.result.isError, false);
    assert.equal(agentRes.json.result.content[0].text, 'Sent to RepoWorker (Studio).');

    const todoRes = await mcp({
      jsonrpc: '2.0',
      id: 7,
      method: 'tools/call',
      params: { name: 'add_todo', arguments: { title: 'Ship Track 4A' } }
    });
    assert.equal(todoRes.json.result.isError, false);
    assert.equal(todoRes.json.result.content[0].text, 'To-do saved: Ship Track 4A');

    const waterRes = await mcp({
      jsonrpc: '2.0',
      id: 8,
      method: 'tools/call',
      params: { name: 'log_water', arguments: { amount_ml: 300 } }
    });
    assert.equal(waterRes.json.result.isError, false);
    assert.equal(waterRes.json.result.content[0].text, 'Logged 300 ml. Today: 800 / 2000 ml.');

    const kbRes = await mcp({
      jsonrpc: '2.0',
      id: 9,
      method: 'tools/call',
      params: { name: 'search_knowledge', arguments: { query: 'Cloud' } }
    });
    assert.equal(kbRes.json.result.isError, false);
    assert.ok(kbRes.json.result.content[0].text.includes('docs/harmony/xiaoyi.md'));

    // Validation & security refusals return isError: true cleanly
    const badWater = await mcp({
      jsonrpc: '2.0',
      id: 10,
      method: 'tools/call',
      params: { name: 'log_water', arguments: { amount_ml: 9999 } }
    });
    assert.equal(badWater.json.result.isError, true);

    const emptyTodo = await mcp({
      jsonrpc: '2.0',
      id: 11,
      method: 'tools/call',
      params: { name: 'add_todo', arguments: { title: '   ' } }
    });
    assert.equal(emptyTodo.json.result.isError, true);

    const approvalRefuse = await mcp({
      jsonrpc: '2.0',
      id: 12,
      method: 'tools/call',
      params: { name: 'ask_persona', arguments: { question: 'Approve pending' } }
    });
    assert.equal(approvalRefuse.json.result.isError, true);
    assert.ok(approvalRefuse.json.result.content[0].text.includes('approval_requires_phone'));

    const recordingRefuse = await mcp({
      jsonrpc: '2.0',
      id: 13,
      method: 'tools/call',
      params: { name: 'ask_persona', arguments: { question: 'Start meeting recording' } }
    });
    assert.equal(recordingRefuse.json.result.isError, true);
    assert.equal(recordingRefuse.json.result.content[0].text, RECORDING_OFF_MESSAGE);
  } finally {
    await cloud.close();
    await hub.close();
  }

  // Recording-enabled hub still defers recording start to the phone (input-required) and water-disabled hub returns MCP isError: true
  const recOnHub = createMockHubServer({ recordingEnabled: true, waterEnabled: false });
  const recOnHubUrl = await recOnHub.listen();
  const recOnCloud = createXiaoYiCloudServer({
    cloudToken: VALID_CLOUD_TOKEN,
    host: '127.0.0.1',
    port: 0,
    hubUrl: recOnHubUrl,
    hubToken: 'hub-secret'
  });
  const { origin: recOnOrigin } = await recOnCloud.listen();
  try {
    const a2aRecOn = await fetch(`${recOnOrigin}/a2a`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${VALID_CLOUD_TOKEN}`
      },
      body: JSON.stringify({
        jsonrpc: '2.0',
        id: 'rec-on',
        method: 'message/send',
        params: { message: { parts: [{ kind: 'text', text: 'Start meeting recording' }] } }
      })
    });
    const a2aRecOnJson = await a2aRecOn.json();
    assert.equal(a2aRecOnJson.result.status.state, 'input-required');
    assert.ok(a2aRecOnJson.result.status.message.parts[0].text.includes('on-device'));

    const mcpWaterOff = await fetch(`${recOnOrigin}/mcp`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${VALID_CLOUD_TOKEN}`
      },
      body: JSON.stringify({
        jsonrpc: '2.0',
        id: 'water-off',
        method: 'tools/call',
        params: { name: 'log_water', arguments: { amount_ml: 250 } }
      })
    });
    const mcpWaterOffJson = await mcpWaterOff.json();
    assert.equal(mcpWaterOffJson.result.isError, true);
    assert.ok(mcpWaterOffJson.result.content[0].text.includes('Water tracking'));
  } finally {
    await recOnCloud.close();
    await recOnHub.close();
  }
});

test('runCloudPreflightCheck and formatCloudPreflightReport report PASS/FAIL and never leak secrets', async () => {
  const hub = createMockHubServer({ recordingEnabled: false });
  const hubUrl = await hub.listen();
  const cfg = {
    cloudToken: 'super-secret-cloud-token-never-logged-1234',
    host: '127.0.0.1',
    port: 18790,
    publicBaseUrl: 'https://cloud.example.com',
    hubUrl,
    hubToken: 'super-secret-hub-token-never-logged-5678'
  };

  try {
    const passReport = await runCloudPreflightCheck(cfg);
    assert.equal(passReport.ok, true);
    assert.equal(passReport.checks.config.ok, true);
    assert.equal(passReport.checks.hubHealth.ok, true);
    assert.equal(passReport.checks.hubAuth.ok, true);
    assert.equal(passReport.checks.assistant.ok, true);
    assert.equal(passReport.checks.recordingModule.allowed, false);

    const formatted = formatCloudPreflightReport(passReport);
    assert.ok(formatted.includes('PASS'));
    assert.ok(!formatted.includes(cfg.cloudToken), 'preflight report must never contain cloudToken');
    assert.ok(!formatted.includes(cfg.hubToken), 'preflight report must never contain hubToken');
  } finally {
    await hub.close();
  }

  const badAuthHub = createMockHubServer({ rejectAuth: true });
  const badUrl = await badAuthHub.listen();
  try {
    const failReport = await runCloudPreflightCheck({ ...cfg, hubUrl: badUrl });
    assert.equal(failReport.ok, false);
    assert.equal(failReport.checks.hubAuth.ok, false);
    assert.ok(failReport.checks.hubAuth.detail.includes('401'));
  } finally {
    await badAuthHub.close();
  }
});
