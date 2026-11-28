import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { EventEmitter, once } from 'node:events'
import { DatabaseSync } from 'node:sqlite'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { chmodSync, mkdirSync, mkdtempSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { parseCompanionArgs } from '../companion.mjs'
import { buildAcpEnv, createOpencodeCompanion } from '../lib/companion.js'

const T0 = Date.now() - 60_000

/** In-memory opencode 1.x SQLite database (`session`, `message`, `part`). */
function fakeV1Db() {
  const db = new DatabaseSync(':memory:')
  db.exec(`
    create table session (
      id text primary key,
      parent_id text,
      directory text not null,
      title text,
      model text,
      revert text,
      time_created integer not null,
      time_updated integer not null,
      time_archived integer
    );
    create table message (
      id text primary key,
      session_id text not null,
      time_created integer not null,
      time_updated integer not null,
      data text not null
    );
    create table part (
      id text primary key,
      message_id text not null,
      session_id text not null,
      time_created integer not null,
      time_updated integer not null,
      data text not null
    );
  `)
  return {
    db,
    session(id, o = {}) {
      db.prepare('insert into session (id, parent_id, directory, title, model, revert, time_created, time_updated, time_archived) values (?,?,?,?,?,?,?,?,?)')
        .run(id, o.parent ?? null, o.directory ?? '/w/app', o.title ?? 'Existing session', o.model ?? null, o.revert ?? null, o.created ?? T0, o.updated ?? Date.now(), o.archived ?? null)
    },
    message(sessionId, msgId, role, parts, extra = {}) {
      const ts = extra.created ?? Date.now()
      db.prepare('insert into message (id, session_id, time_created, time_updated, data) values (?,?,?,?,?)')
        .run(msgId, sessionId, ts, ts, JSON.stringify({ role, time: { created: ts, ...(role === 'assistant' ? { completed: ts + 1 } : {}) }, ...extra }))
      parts.forEach((p, idx) => {
        db.prepare('insert into part (id, message_id, session_id, time_created, time_updated, data) values (?,?,?,?,?,?)')
          .run(`${msgId}_p${idx}`, msgId, sessionId, ts + idx, ts + idx, JSON.stringify(p))
      })
    },
    openDb: async () => ({ all: (sql, ...p) => db.prepare(sql).all(...p), close() {} }),
  }
}

/** Fake OpencodeAcpClient driving companion tests deterministically without spawning opencode. */
class FakeAcpClient extends EventEmitter {
  constructor() {
    super()
    this.generation = 1
    this.pid = 0
    this.busy = false
    this.loadedSessions = new Set()
    this.calls = []
    this.nextId = 1
    this.onPermissionRequest = null
    this.onNewSession = null
    this.onPrompt = null
    this.failModeSet = null
    this.sessionState = new Map()
    this.configModels = [
      { value: 'opencode-go/claude-sonnet-4-6', name: 'opencode-go/Claude Sonnet 4.6' },
      { value: 'openai/gpt-5', name: 'openai/GPT-5' },
    ]
  }
  async ensureStarted() {
    this.calls.push(['ensureStarted'])
    if (!this.pid) {
      this.pid = 40000 + this.generation
      this.generation++
    }
    return { agentCapabilities: { loadSession: true, mcpCapabilities: { http: true } } }
  }
  async stopChild() {
    this.calls.push(['stopChild'])
    this.pid = 0
    this.loadedSessions.clear()
  }
  async newSession({ cwd, mcpServers }) {
    this.calls.push(['newSession', { cwd, mcpServers }])
    const sessionId = `ses_acp_${this.nextId++}`
    this.loadedSessions.add(sessionId)
    if (this.onNewSession) await this.onNewSession({ sessionId, cwd, mcpServers })
    return {
      sessionId,
      configOptions: [
        { id: 'mode', category: 'mode', currentValue: 'build', options: [{ value: 'build', name: 'Build' }, { value: 'plan', name: 'Plan' }] },
        { id: 'model', category: 'model', currentValue: 'opencode-go/claude-sonnet-4-6', options: this.configModels },
      ],
    }
  }
  async loadSession({ sessionId, cwd, mcpServers }) {
    this.calls.push(['loadSession', { sessionId, cwd, mcpServers }])
    this.loadedSessions.add(sessionId)
    return {
      configOptions: [
        { id: 'mode', category: 'mode', currentValue: 'build', options: [{ value: 'build', name: 'Build' }, { value: 'plan', name: 'Plan' }] },
        { id: 'model', category: 'model', currentValue: 'opencode-go/claude-sonnet-4-6', options: this.configModels },
      ],
    }
  }
  async resumeSession(params) {
    this.calls.push(['resumeSession', params])
    this.loadedSessions.add(params.sessionId)
    return { configOptions: [
      { id: 'mode', category: 'mode', currentValue: 'build', options: [{ value: 'build' }, { value: 'plan' }] },
      { id: 'model', category: 'model', currentValue: 'opencode-go/claude-sonnet-4-6', options: this.configModels },
    ] }
  }
  async setConfigOption(sessionId, configId, value) {
    this.calls.push(['setConfigOption', { sessionId, configId, value }])
    if (this.failModeSet && configId === 'mode' && value === this.failModeSet) {
      throw new Error('plan mode unavailable')
    }
    const st = this.sessionState.get(sessionId) || { mode: 'build', model: 'opencode-go/claude-sonnet-4-6', variant: 'low' }
    st[configId] = value
    this.sessionState.set(sessionId, st)
    return {
      configOptions: [
        { id: 'mode', category: 'mode', currentValue: st.mode, options: [{ value: 'build', name: 'Build' }, { value: 'plan', name: 'Plan' }] },
        { id: 'model', category: 'model', currentValue: st.model, options: this.configModels },
        { id: 'variant', category: 'reasoning', currentValue: st.variant, options: [{ value: 'low' }, { value: 'high' }] },
      ],
    }
  }
  async prompt(sessionId, promptBlocks) {
    this.calls.push(['prompt', { sessionId, prompt: promptBlocks }])
    if (this.onPrompt) return this.onPrompt(sessionId, promptBlocks, this)
    this.emit('update', { sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Hel' } } })
    this.emit('update', { sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'lo!' } } })
    return { stopReason: 'end_turn' }
  }
  cancel(sessionId) {
    this.calls.push(['cancel', { sessionId }])
  }
  async closeSession(sessionId) {
    this.calls.push(['closeSession', { sessionId }])
    this.loadedSessions.delete(sessionId)
  }
  async stop() {
    this.loadedSessions.clear()
  }
}

// Legacy feature tests share their expectation log and input controls, but
// each assistant still receives a different ACP object and loaded-session set.
function createTestCompanion(options) {
  const parent = options.acp
  return createOpencodeCompanion({ ...options, assistantAcpFactory: () => {
    const child = new FakeAcpClient()
    child.calls = parent.calls
    for (const key of ['nextId', 'onPrompt', 'onNewSession', 'failModeSet', 'configModels']) {
      Object.defineProperty(child, key, { get: () => parent[key], set: (value) => { parent[key] = value } })
    }
    child.newSession = async (params) => {
      const result = await parent.newSession.call(child, params)
      child.loadedSessions.add(result.sessionId)
      return result
    }
    return child
  } })
}

test('companion advertises launchers/models/assistant without supervision_policy, starts Agent session, streams, handles permissions, modes, rename, close', async (t) => {
  const n = await startFakeNode(t)
  const db = fakeV1Db()
  db.session('ses_disk_1', { directory: '/w/old', title: 'Old TUI session' })
  db.message('ses_disk_1', 'm1', 'user', [{ type: 'text', text: 'from tui' }])
  db.message('ses_disk_1', 'm2', 'assistant', [{ type: 'text', text: 'reply from tui' }], { modelID: 'gpt-5', providerID: 'openai' })

  const acp = new FakeAcpClient()
  const companion = createTestCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    model: 'opencode-go/claude-sonnet-4-6',
    openDb: db.openDb,
    acp,
    discoverModels: async () => [
      { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6', limit: { context: 200000 }, variants: { low: {}, high: {} } },
      { providerID: 'openai', id: 'gpt-5', name: 'GPT-5', limit: { context: 128000 } },
    ],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  assert.equal(n.hello.runtime, 'opencode')
  assert.equal(n.hello.agent, 'opencode')
  assert.equal(n.hello.launcher, 'opencode')
  assert.equal(n.hello.caps.start, true)
  assert.equal(n.hello.caps.models, true)
  assert.equal(n.hello.caps.assistant, true)
  assert.equal(n.hello.caps.readonly, true)
  assert.equal(n.hello.caps.model, true)
  assert.ok(!n.hello.caps.supervision_policy, 'must never declare supervision_policy (#11220)')

  // Disk-only sessions are listed as offline so a running opencode serve plugin wins via prefer_running.
  await until('disk session listed', () => n.last('runtime.sessions')?.sessions?.some((s) => s.session_id === 'ses_disk_1'))
  const diskRow = n.last('runtime.sessions').sessions.find((s) => s.session_id === 'ses_disk_1')
  assert.equal(diskRow.status, 'offline')
  assert.equal(diskRow.title, 'Old TUI session')

  // Reading a foreign session returns not_found.
  await assert.rejects(n.peer.call('session.items', { session_id: 'ses_unknown', limit: 1 }), (err) => err.code === 'not_found')

  // Reading a v1 DB session works cleanly.
  const diskItems = await n.peer.call('session.items', { session_id: 'ses_disk_1', limit: 10 })
  assert.deepEqual(diskItems.items.map((i) => i.text), ['from tui', 'reply from tui'])

  // Models RPC returns normalized catalog.
  const cat = await n.peer.call('launchers.models', { refresh: true })
  assert.deepEqual(cat.models.map((m) => m.id), ['opencode-go/claude-sonnet-4-6', 'openai/gpt-5'])
  assert.deepEqual(cat.models[0].efforts, ['low', 'high'])

  // Start a new Agent session with model, prompt, and image; verify permission card + streaming + tool_call.
  n.resolve = () => ({ allow: true })
  acp.onPrompt = async (sessionId, blocks, client) => {
    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'plan' } } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'tool_call', toolCallId: 'tc_1', kind: 'execute', title: 'bash', rawInput: { command: 'ls' } } })
    const perm = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_1', kind: 'execute', title: 'bash', rawInput: { command: 'ls' } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow once' },
        { optionId: 'always', kind: 'allow_always', name: 'Always allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.deepEqual(perm, { outcome: { outcome: 'selected', optionId: 'once' } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'tool_call_update', toolCallId: 'tc_1', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'file.txt' } }] } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'All good.' } } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'usage_update', used: 1500, size: 200000 } })
    return { stopReason: 'end_turn' }
  }

  const started = await n.peer.call('session.start', {
    cwd: '/w/app',
    model: 'openai/gpt-5',
    prompt: 'inspect dir',
    images: [{ mime: 'image/png', data: 'QUFB' }],
    client_msg_id: 'cm_1',
  })
  const sid = started.session.session_id
  assert.equal(started.session.started_by_persona, true)
  assert.equal(started.session.model, 'openai/gpt-5')
  await n.peer.call('session.watch', { session_id: sid, since_seq: 0 })

  await until('turn finished', () => n.last('runtime.sessions')?.sessions?.find((s) => s.session_id === sid)?.status === 'done')
  assert.equal(n.asked.length, 1)
  assert.equal(n.asked[0].tool_name, 'Bash')
  assert.equal(n.asked[0].summary, 'ls')

  const pg = await n.peer.call('session.items', { session_id: sid, limit: 20 })
  assert.deepEqual(pg.items.map((i) => i.kind), ['text', 'image', 'thinking', 'tool_use', 'tool_result', 'text'])
  assert.equal(pg.items[0].client_msg_id, 'cm_1')
  assert.equal(pg.items[5].text, 'All good.')

  // Lazy image lookup works.
  const img = await n.peer.call('session.image', { session_id: sid, item_id: pg.items[1].id })
  assert.deepEqual(img, { mime: 'image/png', data: 'QUFB' })

  // Switch mode/model via session.modes, rename, and close.
  await n.peer.call('session.modes', { session_id: sid, permission_mode: 'plan', model: 'opencode-go/claude-sonnet-4-6' })
  assert.equal(companion.live.get(sid).mode, 'plan')
  assert.equal(companion.live.get(sid).model, 'opencode-go/claude-sonnet-4-6')

  await n.peer.call('session.rename', { session_id: sid, title: 'Renamed chat' })
  assert.equal(companion.live.get(sid).title, 'Renamed chat')

  await n.peer.call('session.close', { session_id: sid })
  assert.equal(companion.live.has(sid), false)
  assert.ok(acp.calls.some((c) => c[0] === 'closeSession' && c[1].sessionId === sid))
})

test('live ACP keeps the last reply when the next segment is a chunked botmux marker', async (t) => {
  const n = await startFakeNode(t)
  const acp = new FakeAcpClient()
  const companion = createTestCompanion({ socketPath: n.path, openDb: fakeV1Db().openDb, acp, pushDelayMs: 5 })
  t.after(() => companion.stop())
  let streamingPreview
  acp.onPrompt = async (sessionId, blocks, client) => {
    const update = (event) => client.emit('update', { sessionId, update: event })
    const chunk = (text) => update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } })
    chunk('Real words.\nBOTMUX_NOTHING_TO_SEND')
    update({ sessionUpdate: 'tool_call', toolCallId: 'call', kind: 'read', title: 'Read' })
    chunk('BOTMUX_NOTHING_')
    chunk('TO_SEND')
    const before = n.all('runtime.sessions').length
    companion.runtime.sessionsChanged()
    await until('streaming row published', () => n.all('runtime.sessions').length > before)
    streamingPreview = n.last('runtime.sessions').sessions.find(s => s.session_id === sessionId)?.last_text
    return { stopReason: 'end_turn' }
  }
  await companion.start()
  await n.ready
  const { session } = await n.peer.call('session.start', { cwd: '/w/app', prompt: 'go' })
  const id = session.session_id
  await until('turn finished', () => n.last('runtime.sessions')?.sessions?.find(s => s.session_id === id)?.status === 'done')
  assert.equal(streamingPreview, 'Real words.')
  assert.equal(n.last('runtime.sessions').sessions.find(s => s.session_id === id).last_text, 'Real words.')
  const page = await n.peer.call('session.items', { session_id: id, limit: 20 })
  assert.deepEqual(page.items.filter(i => i.role === 'assistant' && i.kind === 'text').map(i => i.text),
    ['Real words.\nBOTMUX_NOTHING_TO_SEND', 'BOTMUX_NOTHING_TO_SEND'])
})

test('the ACP companion exposes A2A tools and forwards calls from its own Agent session', async (t) => {
  const n = await startFakeNode(t, { tools: [{ name: 'a2a_report', description: 'Report a task result' }] })
  const acp = new FakeAcpClient()
  let endpoint
  acp.onNewSession = ({ mcpServers }) => { [endpoint] = mcpServers }
  const companion = createTestCompanion({ socketPath: n.path, openDb: fakeV1Db().openDb, acp })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready
  const { session } = await n.peer.call('session.start', { cwd: '/w/app' })
  const headers = { 'Content-Type': 'application/json', ...Object.fromEntries(endpoint.headers.map(({ name, value }) => [name, value])) }
  const mcp = (method, params) => fetch(endpoint.url, {
    method: 'POST', headers, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }),
  }).then((res) => res.json())
  assert.deepEqual((await mcp('tools/list', {})).result.tools.map(({ name }) => name), ['a2a_report'])
  const args = { task_id: 'task', message_id: 'report', state: 'completed', text: 'Done' }
  assert.equal((await mcp('tools/call', { name: 'a2a_report', arguments: args })).result.content[0].text, 'a2a_report ran')
  assert.deepEqual(n.toolCalls, [{ name: 'a2a_report', args, session_id: session.session_id }])
})

test('companion assistant mode wires HTTP MCP end-to-end, hides context, enforces memory_dir, plan readonly_unsupported, command gates, and rejects supervision_instructions', async (t) => {
  const n = await startFakeNode(t, {
    tools: [
      { name: 'kb_search', description: 'Search KB', input_schema: { type: 'object', properties: {} } },
      { name: 'mail_send', description: 'Send mail', input_schema: { type: 'object', properties: {} }, shell: ['send-mail'] },
    ],
  })
  const db = fakeV1Db()
  const acp = new FakeAcpClient()
  let mcpEndpoint
  acp.onNewSession = async ({ mcpServers }) => {
    mcpEndpoint = mcpServers[0]
    const hdrs = Object.fromEntries(mcpEndpoint.headers.map((h) => [h.name, h.value]))
    // Simulate opencode acp connecting to our HTTP MCP server during session/new (before session.start returns).
    const listRes = await fetch(mcpEndpoint.url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...hdrs },
      body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/list', params: {} }),
    }).then((r) => r.json())
    assert.deepEqual(listRes.result.tools.map((x) => x.name), ['kb_search', 'mail_send'])
  }

  const companion = createTestCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    openDb: db.openDb,
    acp,
    discoverModels: async () => [{ providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' }],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  // Supervision instructions must fail closed (#11220).
  await assert.rejects(
    n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true, supervision_instructions: 'supervision' }),
    (err) => err.code === 'assistant_supervision_runtime',
  )

  // Start assistant session.
  const started = await n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true })
  const sid = started.session.session_id
  assert.ok(mcpEndpoint)

  // Now call a Persona MCP tool over the bound route during a turn.
  const hdrs = Object.fromEntries(mcpEndpoint.headers.map((h) => [h.name, h.value]))
  acp.onPrompt = async (sessionId, blocks, client) => {
    assert.match(blocks[0].text, /^<persona-context>\nnow=2026\n<\/persona-context>\n\n帮我查笔记$/)
    const callRes = await fetch(mcpEndpoint.url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...hdrs },
      body: JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'kb_search', arguments: { q: 'plan' } } }),
    }).then((r) => r.json())
    assert.equal(callRes.result.content[0].text, 'kb_search ran')

    // Command gate must be rejected even under bypassPermissions.
    const gated = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_gate', kind: 'execute', title: 'bash', rawInput: { command: 'send-mail --to alice' } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.deepEqual(gated, { outcome: { outcome: 'selected', optionId: 'reject' } })
    n.setHub(false)
    await until('hub disconnected during native permission', () => !companion.runtime.hubConnected)
    const disconnected = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_offline', kind: 'execute', title: 'bash', rawInput: { command: 'pwd' } },
      options: [{ optionId: 'once', kind: 'allow_once', name: 'Allow' }, { optionId: 'reject', kind: 'reject_once', name: 'Reject' }],
    })
    assert.deepEqual(disconnected, { outcome: { outcome: 'selected', optionId: 'reject' } })
    n.setHub(true)
    await until('hub reconnected during native permission', () => companion.runtime.hubConnected)

    // Memory write auto-allows without asking phone.
    const mem = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_mem', kind: 'edit', title: 'write', rawInput: { filePath: '/home/u/.persona/assistant/memory/a.md' } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.deepEqual(mem, { outcome: { outcome: 'selected', optionId: 'once' } })

    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: '查到了' } } })
    return { stopReason: 'end_turn' }
  }

  await n.peer.call('session.send', {
    session_id: sid,
    text: '帮我查笔记',
    context: 'now=2026',
    assistant: true,
    mode: 'bypassPermissions',
    memory_dir: '/home/u/.persona/assistant/memory',
    client_msg_id: 'cm_asst_1',
  })
  await until('assistant done', () => n.last('runtime.sessions')?.sessions?.find((s) => s.session_id === sid)?.status === 'done')
  assert.equal(n.toolCalls.length, 1)
  assert.equal(n.toolCalls[0].assistant, true)
  assert.equal(n.toolCalls[0].session_id, sid)
  assert.equal(n.asked.length, 0)

  // Context snapshot is hidden from visible items.
  const itemsPage = await n.peer.call('session.items', { session_id: sid, limit: 10 })
  assert.equal(itemsPage.items[0].text, '帮我查笔记')
  assert.equal(itemsPage.items[1].text, '查到了')

  // Failure to enter plan mode surfaces readonly_unsupported.
  acp.failModeSet = 'plan'
  await assert.rejects(
    n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true, mode: 'plan' }),
    (err) => err.code === 'readonly_unsupported',
  )
})

test('companion resumes a disk-only session via ACP session/load and supports interrupt', async (t) => {
  const n = await startFakeNode(t)
  const db = fakeV1Db()
  db.session('ses_resume_1', { directory: '/w/app', title: 'Resume me' })
  db.message('ses_resume_1', 'm1', 'user', [{ type: 'text', text: 'first' }])

  const acp = new FakeAcpClient()
  let resolvePrompt
  acp.onPrompt = () => new Promise((r) => { resolvePrompt = r })

  const companion = createTestCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    openDb: db.openDb,
    acp,
    discoverModels: async () => [],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  await n.peer.call('session.send', { session_id: 'ses_resume_1', text: 'continue', client_msg_id: 'cm_r1' })
  await until('loadSession called', () => acp.calls.some((c) => c[0] === 'loadSession' && c[1].sessionId === 'ses_resume_1'))
  await until('prompt started', () => resolvePrompt !== undefined)

  const intrPromise = n.peer.call('session.interrupt', { session_id: 'ses_resume_1' })
  await until('cancel sent', () => acp.calls.some((c) => c[0] === 'cancel' && c[1].sessionId === 'ses_resume_1'))
  resolvePrompt({ stopReason: 'cancelled' })
  const intr = await intrPromise
  assert.equal(intr.settled, true)

  const pg = await n.peer.call('session.items', { session_id: 'ses_resume_1', limit: 10 })
  assert.equal(pg.items.at(-1).text, '已中断')
})

test('parseCompanionArgs honors --bin, --opencode-bin, --name, --display-name, PERSONA_OPENCODE_BIN, OPENCODE_BIN, and rejects unknown flags', () => {
  const fromDocFlags = parseCompanionArgs(
    ['--bin', '/opt/oc', '--name', 'My OC', '--model', 'openai/gpt-5', '--no-assistant'],
    {},
  )
  assert.equal(fromDocFlags.bin, '/opt/oc')
  assert.equal(fromDocFlags.name, 'My OC')
  assert.equal(fromDocFlags.displayName, 'My OC')
  assert.equal(fromDocFlags.model, 'openai/gpt-5')
  assert.equal(fromDocFlags.assistant, false)

  const fromAliases = parseCompanionArgs(['--opencode-bin', '/usr/bin/oc', '--display-name', 'Alias OC'], {})
  assert.equal(fromAliases.bin, '/usr/bin/oc')
  assert.equal(fromAliases.name, 'Alias OC')
  assert.equal(fromAliases.displayName, 'Alias OC')

  const fromEnv = parseCompanionArgs([], { PERSONA_OPENCODE_BIN: '/env/persona-oc', OPENCODE_BIN: '/env/old-oc' })
  assert.equal(fromEnv.bin, '/env/persona-oc')
  const fromLegacyEnv = parseCompanionArgs([], { OPENCODE_BIN: '/env/old-oc' })
  assert.equal(fromLegacyEnv.bin, '/env/old-oc')

  assert.throws(() => parseCompanionArgs(['--unknown-flag', '1'], {}))
  const bounded = parseCompanionArgs(['--assistant-workers', '3', '--assistant-idle-ms', '2500'], {})
  assert.equal(bounded.maxAssistantWorkers, 3)
  assert.equal(bounded.assistantIdleMs, 2500)
  for (const value of ['0', '65', '1.5', 'NaN']) assert.throws(() => parseCompanionArgs(['--assistant-workers', value], {}))
  for (const value of ['0', '-1', 'Infinity']) assert.throws(() => parseCompanionArgs(['--assistant-idle-ms', value], {}))

  const acpEnv = buildAcpEnv({ OPENCODE_PERMISSION: JSON.stringify({ read: 'allow', bash: 'allow' }) })
  assert.deepEqual(JSON.parse(acpEnv.OPENCODE_PERMISSION), { read: 'allow', bash: 'ask', edit: 'ask', write: 'ask' })
})

test('companion enforces raw unclipped command gates, relative AGENTS.md rejection, start model switch without configOptions, default preservation, plan mode across ACP restart, effort switching, acceptEdits, live thinking, and v2 blob + client_msg_id', async (t) => {
  const n = await startFakeNode(t, {
    tools: [
      { name: 'mail_send', description: 'Send mail', input_schema: { type: 'object', properties: {} }, shell: ['send-mail'] },
    ],
  })

  // Build a v2 DB with a historical session, a blob-backed image, and 2 prior user messages.
  const v2db = new DatabaseSync(':memory:')
  v2db.exec(`
    create table session_v2 (
      id text primary key,
      parent_id text,
      directory text not null,
      title text,
      model text,
      revert text,
      time_created integer not null,
      time_updated integer not null,
      time_archived integer
    );
    create table session_message (
      id text primary key,
      session_id text not null,
      seq integer not null,
      type text not null,
      data text not null,
      time_updated integer not null
    );
    create table blob (
      hash text primary key,
      data blob not null
    );
    create table blob_ref (
      hash text not null,
      session_id text not null,
      message_id text not null,
      seq integer not null
    );
  `)
  const imgBytes = Buffer.from('fake-png-bytes')
  const imgHash = createHash('sha256').update(imgBytes).digest('hex')
  v2db.prepare('insert into session_v2 (id, parent_id, directory, title, model, revert, time_created, time_updated, time_archived) values (?,?,?,?,?,?,?,?,?)')
    .run('ses_v2_hist', null, '/w/v2', 'V2 Hist', null, null, T0, Date.now(), null)
  v2db.prepare('insert into session_message (id, session_id, seq, type, data, time_updated) values (?,?,?,?,?,?)')
    .run('u1', 'ses_v2_hist', 1, 'user', JSON.stringify({ text: 'first msg', files: [{ mime: 'image/png', uri: `media:${imgHash}` }] }), Date.now())
  v2db.prepare('insert into session_message (id, session_id, seq, type, data, time_updated) values (?,?,?,?,?,?)')
    .run('a1', 'ses_v2_hist', 2, 'assistant', JSON.stringify({ content: [{ type: 'text', text: 'first reply' }] }), Date.now())
  v2db.prepare('insert into blob (hash, data) values (?,?)').run(imgHash, imgBytes)
  v2db.prepare('insert into blob_ref (hash, session_id, message_id, seq) values (?,?,?,?)').run(imgHash, 'ses_v2_hist', 'u1', 1)

  const acp = new FakeAcpClient()
  let returnEmptyConfigOnNew = false
  const origNew = acp.newSession.bind(acp)
  acp.newSession = async (args) => {
    if (returnEmptyConfigOnNew) {
      acp.calls.push(['newSession', args])
      const sessionId = `ses_acp_${acp.nextId++}`
      acp.loadedSessions.add(sessionId)
      return { sessionId } // No configOptions returned!
    }
    const res = await origNew(args)
    res.configOptions.push({
      id: 'variant',
      category: 'reasoning',
      currentValue: 'low',
      options: [{ value: 'low' }, { value: 'high' }],
    })
    return res
  }

  const companion = createTestCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    // No explicit default model! CLI catalog's first model differs from ACP's currentValue.
    openDb: async () => ({ all: (sql, ...p) => v2db.prepare(sql).all(...p), close() {} }),
    acp,
    discoverModels: async () => [
      { providerID: 'openai', id: 'gpt-5', name: 'GPT-5', variants: { low: {}, high: {} } },
      { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6', variants: { low: {}, high: {} } },
    ],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready
  await n.peer.call('launchers.models', { refresh: true })

  // 1. Blob-backed 2.x image loads in companion.
  const histItems = await n.peer.call('session.items', { session_id: 'ses_v2_hist', limit: 10 })
  const imgItem = histItems.items.find((i) => i.kind === 'image')
  assert.ok(imgItem)
  const blobImg = await n.peer.call('session.image', { session_id: 'ses_v2_hist', item_id: imgItem.id })
  assert.deepEqual(blobImg, { mime: 'image/png', data: imgBytes.toString('base64') })

  // 2. Resumed v2 session initializes userInputCount from DB so client_msg_id attaches to the new message (ordinal 2).
  acp.onPrompt = async (sessionId) => {
    v2db.prepare('insert into session_message (id, session_id, seq, type, data, time_updated) values (?,?,?,?,?,?)')
      .run('u2', sessionId, 3, 'user', JSON.stringify({ text: 'second msg' }), Date.now())
    v2db.prepare('insert into session_message (id, session_id, seq, type, data, time_updated) values (?,?,?,?,?,?)')
      .run('a2', sessionId, 4, 'assistant', JSON.stringify({ content: [{ type: 'text', text: 'second reply' }] }), Date.now())
    return { stopReason: 'end_turn' }
  }
  await n.peer.call('session.send', { session_id: 'ses_v2_hist', text: 'second msg', client_msg_id: 'cm_v2_2' })
  await until('v2 turn done', () => !companion.live.get('ses_v2_hist')?.running)
  const v2After = await n.peer.call('session.items', { session_id: 'ses_v2_hist', limit: 10 })
  const secondUser = v2After.items.find((i) => i.text === 'second msg')
  assert.equal(secondUser?.client_msg_id, 'cm_v2_2')

  // 3. Starting without an explicit default model must NOT override ACP session/new's currentValue with models[0].
  acp.calls.length = 0
  const defStart = await n.peer.call('session.start', { cwd: '/w/app' })
  assert.equal(defStart.session.model, 'opencode-go/claude-sonnet-4-6')
  assert.equal(acp.calls.filter((c) => c[0] === 'setConfigOption' && c[1].configId === 'model').length, 0)

  // 4. Starting with a requested model when session/new returns NO configOptions MUST still call setConfigOption('model', ...).
  returnEmptyConfigOnNew = true
  acp.calls.length = 0
  const explicitStart = await n.peer.call('session.start', { cwd: '/w/app', model: 'openai/gpt-5', effort: 'high' })
  assert.equal(explicitStart.session.model, 'openai/gpt-5')
  assert.equal(explicitStart.session.effort, 'high')
  assert.ok(acp.calls.some((c) => c[0] === 'setConfigOption' && c[1].configId === 'model' && c[1].value === 'openai/gpt-5'))
  assert.ok(acp.calls.some((c) => c[0] === 'setConfigOption' && c[1].configId === 'variant' && c[1].value === 'high'))
  returnEmptyConfigOnNew = false

  // 5. Plan mode survives an ACP child restart (ensureLoaded re-applies mode=plan).
  const planStart = await n.peer.call('session.start', { cwd: '/w/app', permission_mode: 'plan' })
  const planSid = planStart.session.session_id
  assert.equal(companion.live.get(planSid).mode, 'plan')
  acp.generation++
  acp.loadedSessions.clear()
  acp.calls.length = 0
  acp.onPrompt = async () => ({ stopReason: 'end_turn' })
  await n.peer.call('session.send', { session_id: planSid, text: 'check plan' })
  await until('plan turn done', () => !companion.live.get(planSid)?.running)
  assert.equal(companion.live.get(planSid).mode, 'plan')
  assert.ok(acp.calls.some((c) => c[0] === 'setConfigOption' && c[1].sessionId === planSid && c[1].configId === 'mode' && c[1].value === 'plan'))

  // 6. Long 26KB padded command + shell syntax (`send-mail; echo`) + relative AGENTS.md write + acceptEdits + live thinking.
  const asstStart = await n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true })
  const asstSid = asstStart.session.session_id
  await n.peer.call('session.watch', { session_id: asstSid, since_seq: 0 })
  n.asked.length = 0
  n.resolve = () => ({ allow: false })

  acp.onPrompt = async (sessionId, _blocks, client) => {
    // Live thinking streams incrementally.
    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'step 1 ' } } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'step 2' } } })

    // 26KB padded command ending in `; send-mail` (clipInput would truncate to 12KB) must STILL be rejected under bypassPermissions!
    const longCmd = `echo ${'a'.repeat(26_000)}; "send-mail" --to alice`
    const gated = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_long', kind: 'execute', title: 'bash', rawInput: { command: longCmd } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.deepEqual(gated, { outcome: { outcome: 'selected', optionId: 'reject' } })

    // Switch to default mode and check relative `AGENTS.md` is NOT auto-approved as memory write.
    companion.live.get(sessionId).mode = 'default'
    const relAgents = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_agents', kind: 'edit', title: 'write', rawInput: { filePath: 'AGENTS.md' } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.equal(n.asked.length, 1, 'relative AGENTS.md must ask phone')
    assert.deepEqual(relAgents, { outcome: { outcome: 'selected', optionId: 'reject' } })

    // acceptEdits auto-approves write/edit without asking phone.
    companion.live.get(sessionId).mode = 'acceptEdits'
    const editOk = await client.onPermissionRequest({
      sessionId,
      toolCall: { toolCallId: 'tc_edit_ok', kind: 'edit', title: 'edit', rawInput: { filePath: '/w/code.js' } },
      options: [
        { optionId: 'once', kind: 'allow_once', name: 'Allow' },
        { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
      ],
    })
    assert.deepEqual(editOk, { outcome: { outcome: 'selected', optionId: 'once' } })
    assert.equal(n.asked.length, 1)

    return { stopReason: 'end_turn' }
  }

  await n.peer.call('session.send', {
    session_id: asstSid,
    text: 'run checks',
    assistant: true,
    mode: 'bypassPermissions',
    memory_dir: '/home/u/.persona/assistant/memory',
  })
  await until('asst checks done', () => !companion.live.get(asstSid)?.running)
  const thoughtItems = (await n.peer.call('session.items', { session_id: asstSid, limit: 10 })).items.filter((i) => i.kind === 'thinking')
  assert.equal(thoughtItems.length, 1)
  assert.equal(thoughtItems[0].text, 'step 1 step 2')
})

test('parseCompanionArgs supports --instance, --no-update, and --parent-stdin with strict validation', () => {
  const parsed = parseCompanionArgs([
    '--socket', '/tmp/runtime.sock',
    '--bin', '/usr/local/bin/opencode',
    '--instance', 'builtin-opencode-42-1',
    '--no-update',
    '--parent-stdin',
  ], {})
  assert.equal(parsed.socketPath, '/tmp/runtime.sock')
  assert.equal(parsed.bin, '/usr/local/bin/opencode')
  assert.equal(parsed.instance, 'builtin-opencode-42-1')
  assert.equal(parsed.noUpdate, true)
  assert.equal(parsed.parentStdin, true)
  assert.equal(parsed.assistant, true)
  assert.throws(() => parseCompanionArgs(['--unknown-flag'], {}))
})

test('companion.mjs with --parent-stdin exits cleanly when parent closes stdin', async () => {
  const script = fileURLToPath(new URL('../companion.mjs', import.meta.url))
  const child = spawn(process.execPath, [
    script,
    '--socket', '/tmp/persona-opencode-nonexistent-test.sock',
    '--bin', '/nonexistent/opencode-test-bin',
    '--instance', 'builtin-opencode-test-1',
    '--no-update',
    '--parent-stdin',
  ], {
    stdio: ['pipe', 'pipe', 'pipe'],
  })
  let stderr = ''
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', (chunk) => { stderr += chunk })

  await until('companion started', () => stderr.includes('started opencode ACP companion for Persona'), 5000)
  child.stdin.end()

  const exit = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      try { child.kill('SIGKILL') } catch {}
      reject(new Error(`companion did not exit after stdin closed; stderr=${stderr}`))
    }, 5000)
    child.once('exit', (code, signal) => {
      clearTimeout(timer)
      resolve({ code, signal })
    })
  })
  assert.equal(exit.code, 0)
  assert.equal(exit.signal, null)
})

// #12848: Node loads the entry module by its real path while argv[1] keeps a symlink on the way (a symlinked home is
// common on Linux, and the node starts the companion from under ~/.persona), so an entry check comparing the two as given
// skipped main() and the companion exited 0 without a word, once a minute.
test('companion.mjs runs main() when started through a symlinked directory', async (t) => {
  const base = realpathSync(mkdtempSync(join(tmpdir(), 'persona-opencode-link-')))
  const pkg = fileURLToPath(new URL('..', import.meta.url))
  const link = join(base, 'opencode')
  symlinkSync(pkg, link, process.platform === 'win32' ? 'junction' : 'dir')
  const child = spawn(process.execPath, [
    join(link, 'companion.mjs'),
    '--socket', join(base, 'missing', 'runtime.sock'),
    '--bin', '/nonexistent/opencode-test-bin',
    '--instance', 'builtin-opencode-test-link',
    '--no-update',
    '--parent-stdin',
  ], { stdio: ['pipe', 'pipe', 'pipe'] })
  const exited = once(child, 'exit')
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL')
    await exited
    chmodSync(base, 0o700)
    rmSync(base, { recursive: true, force: true })
  })
  let stderr = ''
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', (chunk) => { stderr += chunk })

  await until('companion started or exited', () => stderr.includes('started opencode ACP companion for Persona') || child.exitCode !== null, 5000)
  assert.equal(child.exitCode, null, `companion exited (${child.exitCode}) instead of starting; stderr=${JSON.stringify(stderr)}`)
  child.stdin.end()
  const [code] = await exited
  assert.equal(code, 0)
})

test('companion does not spawn opencode acp on startup, stops idle acp child, and reloads on next turn', async (t) => {
  const n = await startFakeNode(t)
  const db = fakeV1Db()
  const acp = new FakeAcpClient()
  const companion = createOpencodeCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    instance: 'builtin-opencode-idle-1',
    acpIdleMs: 40,
    openDb: db.openDb,
    acp,
    discoverModels: async () => [{ providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' }],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  // Hello carries the configured --instance and never declares supervision_policy.
  assert.equal(n.hello.instance, 'builtin-opencode-idle-1')
  assert.equal(n.hello.caps?.supervision_policy, undefined)

  // Idle startup never spawns opencode acp.
  assert.equal(acp.pid, 0)
  assert.equal(acp.calls.filter((c) => c[0] === 'ensureStarted').length, 0)

  // Starting a session spawns acp; once idle, acp.stopChild() reclaims it.
  const started = await n.peer.call('session.start', { cwd: '/w/app', prompt: 'hello' })
  const sid = started.session.session_id
  await until('first turn done', () => !companion.live.get(sid)?.running)
  await until('idle acp child stopped', () => acp.pid === 0 && acp.calls.some((c) => c[0] === 'stopChild'), 2000)

  // Next send transparently restarts acp and reloads the session via session/load.
  acp.calls.length = 0
  await n.peer.call('session.send', { session_id: sid, text: 'follow up' })
  await until('second turn done', () => !companion.live.get(sid)?.running)
  assert.ok(acp.calls.some((c) => c[0] === 'ensureStarted'))
  assert.ok(acp.calls.some((c) => c[0] === 'loadSession' && c[1].sessionId === sid))
  await until('idle acp child stopped again', () => acp.pid === 0 && acp.calls.some((c) => c[0] === 'stopChild'), 2000)
})

test('live ACP tool_call notifications normalize tool_name and tool_input from rawInput and kind', async (t) => {
  const n = await startFakeNode(t)
  const db = fakeV1Db()
  const acp = new FakeAcpClient()
  acp.onPrompt = async (sessionId, _blocks, client) => {
    client.emit('update', {
      sessionId,
      update: {
        sessionUpdate: 'tool_call',
        toolCallId: 'tc_edit',
        kind: 'edit',
        title: 'Edit src/app.ts',
        rawInput: { filePath: '/w/app/src/app.ts', oldString: 'old', newString: 'new' },
      },
    })
    client.emit('update', {
      sessionId,
      update: {
        sessionUpdate: 'tool_call',
        toolCallId: 'tc_search',
        kind: 'search',
        title: 'Search auth token',
        rawInput: { pattern: 'auth', path: '/w/app' },
      },
    })
    client.emit('update', {
      sessionId,
      update: {
        sessionUpdate: 'tool_call',
        toolCallId: 'tc_fetch',
        kind: 'fetch',
        title: 'Fetch https://example.com',
        rawInput: { url: 'https://example.com', format: 'markdown' },
      },
    })
    return { stopReason: 'end_turn' }
  }
  const companion = createTestCompanion({ socketPath: n.path, openDb: db.openDb, acp })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  const started = await n.peer.call('session.start', { cwd: '/w/app', prompt: 'edit and search' })
  const sid = started.session.session_id
  await until('turn finished', () => !companion.live.get(sid)?.running)

  const pg = await n.peer.call('session.items', { session_id: sid, limit: 20 })
  const tools = pg.items.filter((i) => i.kind === 'tool_use')
  assert.deepEqual(
    tools.map((i) => [i.tool_name, i.tool_input]),
    [
      ['Edit', { file_path: '/w/app/src/app.ts', old_string: 'old', new_string: 'new' }],
      ['Grep', { pattern: 'auth', path: '/w/app' }],
      ['WebFetch', { url: 'https://example.com', format: 'markdown' }],
    ],
  )
})

test('session.interrupt cancels queued turns on normal sessions, settles immediately, and clears working status', async (t) => {
  const n = await startFakeNode(t)
  const db = fakeV1Db()
  const acp = new FakeAcpClient()
  let turn1Started
  const turn1Entered = new Promise((r) => { turn1Started = r })
  let finishTurn1
  let finishUnexpectedTurn2
  t.after(() => finishUnexpectedTurn2?.({ stopReason: 'end_turn' }))
  acp.cancel = (sessionId) => {
    acp.calls.push(['cancel', { sessionId }])
    finishTurn1?.({ stopReason: 'cancelled' })
  }
  acp.onPrompt = async (_sessionId, blocks) => {
    const text = blocks.map((b) => b.text).join('')
    if (text === 'first turn') {
      turn1Started()
      return new Promise((r) => { finishTurn1 = r })
    }
    // If a queued second turn executes after interrupt, hold it until the test finishes so it is immediately obvious.
    return new Promise((r) => { finishUnexpectedTurn2 = r })
  }
  const companion = createTestCompanion({ socketPath: n.path, openDb: db.openDb, acp })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  const started = await n.peer.call('session.start', { cwd: '/w/app', prompt: 'first turn' })
  const sid = started.session.session_id
  await turn1Entered

  // Queue a second message while Turn 1 is still running on a normal (non-assistant) session.
  const queued = await n.peer.call('session.send', { session_id: sid, text: 'queued follow-up' })
  assert.equal(queued.queued, true)
  assert.equal(companion.live.get(sid)?.queued, 1)

  // Interrupt must cancel Turn 1, skip the queued follow-up, settle promptly, and push non-working status.
  const intr = await n.peer.call('session.interrupt', { session_id: sid })
  assert.equal(intr.settled, true)
  assert.equal(companion.live.get(sid)?.running, false)
  assert.equal(companion.live.get(sid)?.queued, 0)

  const prompts = acp.calls.filter((c) => c[0] === 'prompt').map((c) => c[1].prompt.map((b) => b.text).join(''))
  assert.deepEqual(prompts, ['first turn'])

  await until('final pushed session status leaves working', () => {
    const latest = n.last('runtime.sessions')?.sessions?.find((r) => r.session_id === sid)
    return latest && latest.status !== 'working'
  }, 1500)
  const latest = n.last('runtime.sessions')?.sessions?.find((r) => r.session_id === sid)
  assert.equal(latest?.status, 'done')
})

test('session.interrupt skips Turn 2 without waiting for an immediate Turn 3 and schedules idle stop after skipping queued turns', async (t) => {
  const n = await startFakeNode(t)
  const db = fakeV1Db()
  const acp = new FakeAcpClient()
  let turn1Started
  const turn1Entered = new Promise((r) => { turn1Started = r })
  let turn1CancelSent
  const turn1Cancelled = new Promise((r) => { turn1CancelSent = r })
  let finishTurn1
  let turn3Started
  const turn3Entered = new Promise((r) => { turn3Started = r })
  let finishTurn3
  t.after(() => {
    finishTurn1?.({ stopReason: 'cancelled' })
    finishTurn3?.({ stopReason: 'end_turn' })
  })
  acp.cancel = (sessionId) => {
    acp.calls.push(['cancel', { sessionId }])
    turn1CancelSent?.()
  }
  acp.onPrompt = async (_sessionId, blocks) => {
    const text = blocks.map((b) => b.text).join('')
    if (text === 'turn 1') {
      turn1Started()
      return new Promise((r) => { finishTurn1 = r })
    }
    if (text === 'turn 3') {
      turn3Started()
      return new Promise((r) => { finishTurn3 = r })
    }
    throw new Error(`unexpected prompt ${text}`)
  }
  const companion = createOpencodeCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    acpIdleMs: 40,
    openDb: db.openDb,
    acp,
    discoverModels: async () => [{ providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' }],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  const started = await n.peer.call('session.start', { cwd: '/w/app', prompt: 'turn 1' })
  const sid = started.session.session_id
  await turn1Entered

  // Queue Turn 2 while Turn 1 is active.
  const q2 = await n.peer.call('session.send', { session_id: sid, text: 'turn 2 (should be skipped)' })
  assert.equal(q2.queued, true)

  // Start interrupt (holds until Turn 1 cancels and Turn 2 skips), then immediately send Turn 3 before Turn 1 settles.
  const intrPromise = n.peer.call('session.interrupt', { session_id: sid })
  await turn1Cancelled
  const q3 = await n.peer.call('session.send', { session_id: sid, text: 'turn 3' })
  assert.equal(q3.queued, true)

  // Now complete Turn 1 cancellation: Turn 2 skips, interrupt resolves without waiting for Turn 3, and Turn 3 begins.
  finishTurn1({ stopReason: 'cancelled' })
  const intr = await intrPromise
  assert.equal(intr.settled, true)
  await turn3Entered

  // Finish Turn 3 and verify only Turn 1 and Turn 3 ever reached acp.prompt.
  finishTurn3({ stopReason: 'end_turn' })
  await until('turn 3 done', () => !companion.live.get(sid)?.running && companion.live.get(sid)?.queued === 0)
  const prompts = acp.calls.filter((c) => c[0] === 'prompt').map((c) => c[1].prompt.map((b) => b.text).join(''))
  assert.deepEqual(prompts, ['turn 1', 'turn 3'])

  // Now verify scheduleAcpIdleStop() fires cleanly after skipping a queued turn (no subsequent Turn 3).
  acp.calls.length = 0
  let finishA
  let aStarted
  const aEntered = new Promise((r) => { aStarted = r })
  acp.cancel = (sessionId) => {
    acp.calls.push(['cancel', { sessionId }])
    finishA?.({ stopReason: 'cancelled' })
  }
  acp.onPrompt = async (_sessionId, blocks) => {
    const text = blocks.map((b) => b.text).join('')
    if (text === 'turn A') {
      aStarted()
      return new Promise((r) => { finishA = r })
    }
    throw new Error(`unexpected prompt ${text}`)
  }
  await n.peer.call('session.send', { session_id: sid, text: 'turn A' })
  await aEntered
  await n.peer.call('session.send', { session_id: sid, text: 'turn B (skipped)' })
  const intr2 = await n.peer.call('session.interrupt', { session_id: sid })
  assert.equal(intr2.settled, true)
  await until('idle acp child stopped after skipping queued turn', () => acp.pid === 0 && acp.calls.some((c) => c[0] === 'stopChild'), 2000)
})

test('session.interrupt and session.close during pre-prompt setup prevent acp.prompt, stale snapshots, and leaks', async (t) => {
  const n = await startFakeNode(t)
  const db = fakeV1Db()
  const acp = new FakeAcpClient()
  const companion = createOpencodeCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: '/home/u',
    acpIdleMs: 0,
    openDb: db.openDb,
    acp,
    discoverModels: async () => [
      { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6', variants: { low: {}, high: {} } },
      { providerID: 'openai', id: 'gpt-5', name: 'GPT-5', variants: { low: {}, high: {} } },
    ],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  const started = await n.peer.call('session.start', { cwd: '/w/app' })
  const sid = started.session.session_id

  // 1) Interrupt while acp.ensureStarted is pending: must skip loadSession, setConfigOption, and acp.prompt,
  //    leave loadedGeneration cleared, and allow an immediate follow-up send() to succeed cleanly (status working -> done).
  acp.loadedSessions.delete(sid)
  companion.live.get(sid).loadedGeneration = 0
  acp.calls.length = 0
  let releaseStart1
  let start1EnteredResolve
  const start1Entered = new Promise((r) => { start1EnteredResolve = r })
  let cancelNotified
  let cancelSeen = new Promise((r) => { cancelNotified = r })
  acp.cancel = (sessionId) => {
    acp.calls.push(['cancel', { sessionId }])
    cancelNotified?.()
  }
  const origEnsureStarted = acp.ensureStarted.bind(acp)
  acp.ensureStarted = async () => {
    start1EnteredResolve()
    await new Promise((r) => { releaseStart1 = r })
    return origEnsureStarted()
  }
  t.after(() => releaseStart1?.())

  await n.peer.call('session.send', { session_id: sid, text: 'cancel during ensureStarted', model: 'openai/gpt-5' })
  await start1Entered
  const intrStartPromise = n.peer.call('session.interrupt', { session_id: sid })
  await cancelSeen
  releaseStart1()
  const intrStart = await intrStartPromise
  assert.equal(intrStart.settled, true)
  assert.equal(companion.live.get(sid).loadedGeneration, 0, 'loadedGeneration must remain cleared on pre-prompt cancel')
  assert.equal(acp.calls.filter((c) => c[0] === 'loadSession').length, 0, 'loadSession must not run after interrupt during ensureStarted')
  assert.equal(acp.calls.filter((c) => c[0] === 'setConfigOption').length, 0, 'setConfigOption must not run after interrupt during ensureStarted')
  assert.equal(acp.calls.filter((c) => c[0] === 'prompt').length, 0, 'acp.prompt must not run after interrupt during ensureStarted')
  await until('status leaves working after pre-prompt interrupt', () => {
    const row = n.last('runtime.sessions')?.sessions?.find((r) => r.session_id === sid)
    return row && row.status === 'done'
  }, 1500)

  // Immediate follow-up turn right after pre-prompt interrupt must load cleanly and complete.
  acp.ensureStarted = origEnsureStarted
  acp.calls.length = 0
  await n.peer.call('session.send', { session_id: sid, text: 'follow-up after pre-prompt cancel' })
  await until('follow-up turn done', () => !companion.live.get(sid)?.running)
  assert.equal(acp.calls.filter((c) => c[0] === 'loadSession').length, 1)
  assert.deepEqual(
    acp.calls.filter((c) => c[0] === 'prompt').map((c) => c[1].prompt.map((b) => b.text).join('')),
    ['follow-up after pre-prompt cancel'],
  )
  await until('status returns to done after follow-up turn', () => {
    const row = n.last('runtime.sessions')?.sessions?.find((r) => r.session_id === sid)
    return row && row.status === 'done'
  }, 1500)

  // 2) Interrupt while acp.loadSession is pending: must skip setConfigOption and acp.prompt and keep loadedGeneration cleared.
  acp.loadedSessions.delete(sid)
  companion.live.get(sid).loadedGeneration = 0
  acp.calls.length = 0
  cancelSeen = new Promise((r) => { cancelNotified = r })
  let releaseLoad
  let loadStarted
  const loadEntered = new Promise((r) => { loadStarted = r })
  const origLoad = acp.loadSession.bind(acp)
  acp.loadSession = async (opts) => {
    loadStarted()
    await new Promise((r) => { releaseLoad = r })
    return origLoad(opts)
  }
  t.after(() => releaseLoad?.())

  await n.peer.call('session.send', { session_id: sid, text: 'cancel during load', model: 'openai/gpt-5' })
  await loadEntered
  const intrLoadPromise = n.peer.call('session.interrupt', { session_id: sid })
  await cancelSeen
  releaseLoad()
  const intrLoad = await intrLoadPromise
  assert.equal(intrLoad.settled, true)
  assert.equal(companion.live.get(sid).loadedGeneration, 0)
  assert.equal(acp.calls.filter((c) => c[0] === 'prompt').length, 0, 'acp.prompt must not run after interrupt during loadSession')
  assert.equal(acp.calls.filter((c) => c[0] === 'setConfigOption').length, 0, 'applySessionConfig must not run after interrupt during loadSession')
  const itemsAfterLoadCancel = await n.peer.call('session.items', { session_id: sid })
  assert.ok(itemsAfterLoadCancel.items.some((it) => it.kind === 'notice' && it.text === '已中断'))

  // 3) Interrupt mid-applySessionConfig multi-step (mode + model + effort):
  //    pausing on the first setConfigOption (mode) must skip model, effort, and acp.prompt.
  acp.loadSession = origLoad
  acp.calls.length = 0
  cancelSeen = new Promise((r) => { cancelNotified = r })
  let releaseConfig
  let configStarted
  const configEntered = new Promise((r) => { configStarted = r })
  const origSetConfig = acp.setConfigOption.bind(acp)
  acp.setConfigOption = async (sessionId, configId, value) => {
    if (configId === 'mode') {
      configStarted()
      await new Promise((r) => { releaseConfig = r })
    }
    return origSetConfig(sessionId, configId, value)
  }
  t.after(() => releaseConfig?.())

  await n.peer.call('session.send', {
    session_id: sid,
    text: 'cancel during multi-step config',
    mode: 'plan',
    model: 'openai/gpt-5',
    effort: 'high',
  })
  await configEntered
  const intrConfigPromise = n.peer.call('session.interrupt', { session_id: sid })
  await cancelSeen
  releaseConfig()
  const intrConfig = await intrConfigPromise
  assert.equal(intrConfig.settled, true)
  assert.deepEqual(
    acp.calls.filter((c) => c[0] === 'setConfigOption').map((c) => c[1].configId),
    ['mode'],
    'model and effort setConfigOption must not run after interrupt during mode step',
  )
  assert.equal(acp.calls.filter((c) => c[0] === 'prompt').length, 0, 'acp.prompt must not run after interrupt during applySessionConfig')

  // 4) session.modes closed mid-applySessionConfig must reject with code "closed" (not silent ok).
  acp.calls.length = 0
  let releaseModesConfig
  let modesConfigEnteredResolve
  const modesConfigEntered = new Promise((r) => { modesConfigEnteredResolve = r })
  acp.setConfigOption = async (sessionId, configId, value) => {
    modesConfigEnteredResolve()
    await new Promise((r) => { releaseModesConfig = r })
    return origSetConfig(sessionId, configId, value)
  }
  t.after(() => releaseModesConfig?.())
  const modesStarted = await n.peer.call('session.start', { cwd: '/w/app' })
  const modesSid = modesStarted.session.session_id
  const modesPromise = n.peer.call('session.modes', { session_id: modesSid, model: 'openai/gpt-5' })
  await modesConfigEntered
  await n.peer.call('session.close', { session_id: modesSid })
  releaseModesConfig()
  await assert.rejects(modesPromise, (err) => err.code === 'closed')

  // 5) Close while acp.loadSession is pending: must call closeSession, release mcpRoute, skip setConfigOption/prompt, and never call runtime.append after close.
  acp.setConfigOption = origSetConfig
  const loadCloseStarted = await n.peer.call('session.start', { cwd: '/w/app' })
  const loadCloseSid = loadCloseStarted.session.session_id
  const loadCloseState = companion.live.get(loadCloseSid)
  const routeBeforeClose = loadCloseState.mcpRoute
  let routeReleased = 0
  if (routeBeforeClose) {
    const origRelease = routeBeforeClose.release.bind(routeBeforeClose)
    routeBeforeClose.release = () => { routeReleased++; return origRelease() }
  }
  acp.loadedSessions.delete(loadCloseSid)
  loadCloseState.loadedGeneration = 0
  acp.calls.length = 0
  let releaseLoadClose
  let loadCloseEnteredResolve
  const loadCloseEntered = new Promise((r) => { loadCloseEnteredResolve = r })
  acp.loadSession = async (opts) => {
    loadCloseEnteredResolve()
    await new Promise((r) => { releaseLoadClose = r })
    return origLoad(opts)
  }
  t.after(() => releaseLoadClose?.())

  let appendsAfterClose = 0
  let closedTrackingId = ''
  const origRuntimeAppend = companion.runtime.append
  companion.runtime.append = (sessionId, payload) => {
    if (sessionId === closedTrackingId) appendsAfterClose++
    return origRuntimeAppend(sessionId, payload)
  }
  t.after(() => { companion.runtime.append = origRuntimeAppend })

  await n.peer.call('session.watch', { session_id: loadCloseSid })
  await n.peer.call('session.send', { session_id: loadCloseSid, text: 'close during loadSession', model: 'openai/gpt-5' })
  await loadCloseEntered
  await n.peer.call('session.close', { session_id: loadCloseSid })
  const itemsLenAtLoadClose = loadCloseState.liveItems.length
  closedTrackingId = loadCloseSid
  appendsAfterClose = 0
  const closeCallsBeforeRelease = acp.calls.filter((c) => c[0] === 'closeSession' && c[1].sessionId === loadCloseSid).length
  releaseLoadClose()
  await loadCloseState.tail.catch(() => {})
  assert.equal(loadCloseState.running, false)
  assert.ok(routeReleased >= 1, 'mcpRoute must be released on post-loadSession close')
  assert.equal(
    acp.calls.filter((c) => c[0] === 'closeSession' && c[1].sessionId === loadCloseSid).length,
    closeCallsBeforeRelease + 1,
    'post-loadSession close must call acp.closeSession to clean up newly loaded session',
  )
  assert.equal(acp.calls.filter((c) => c[0] === 'setConfigOption' && c[1].sessionId === loadCloseSid).length, 0)
  assert.equal(acp.calls.filter((c) => c[0] === 'prompt' && c[1].sessionId === loadCloseSid).length, 0)
  assert.equal(appendsAfterClose, 0, 'runtime.append must not be called after session.close')
  assert.equal(loadCloseState.liveItems.length, itemsLenAtLoadClose, 'liveItems must not grow after session.close')

  // 6) Close while acp.ensureStarted is pending: must skip loadSession, prompt, and any post-close runtime.append.
  acp.loadSession = origLoad
  acp.loadedSessions.delete(sid)
  companion.live.get(sid).loadedGeneration = 0
  acp.calls.length = 0
  let releaseStartClose
  let startCloseEnteredResolve
  const startCloseEntered = new Promise((r) => { startCloseEnteredResolve = r })
  acp.ensureStarted = async () => {
    startCloseEnteredResolve()
    await new Promise((r) => { releaseStartClose = r })
    return origEnsureStarted()
  }
  t.after(() => releaseStartClose?.())

  const closedState = companion.live.get(sid)
  await n.peer.call('session.watch', { session_id: sid })
  await n.peer.call('session.send', { session_id: sid, text: 'close during start' })
  await startCloseEntered
  await n.peer.call('session.close', { session_id: sid })
  const itemsLenAtStartClose = closedState.liveItems.length
  closedTrackingId = sid
  appendsAfterClose = 0
  releaseStartClose()
  await closedState.tail.catch(() => {})
  assert.equal(closedState.running, false)
  assert.equal(acp.calls.filter((c) => c[0] === 'loadSession').length, 0, 'loadSession must not run after session.close')
  assert.equal(acp.calls.filter((c) => c[0] === 'prompt').length, 0, 'acp.prompt must not run after session.close')
  assert.equal(appendsAfterClose, 0, 'runtime.append must not be called after session.close')
  assert.equal(closedState.liveItems.length, itemsLenAtStartClose, 'liveItems must not grow after session.close')

  // 7) Close while an activeThoughtItem is live during acp.prompt:
  //    flushLiveText -> flushActiveThought -> pushLiveUpdate must NOT call runtime.append or rt.streamEnd after close.
  acp.ensureStarted = origEnsureStarted
  const thoughtStarted = await n.peer.call('session.start', { cwd: '/w/app' })
  const thoughtSid = thoughtStarted.session.session_id
  const thoughtState = companion.live.get(thoughtSid)
  let releaseThoughtPrompt
  let thoughtEmittedResolve
  const thoughtEmitted = new Promise((r) => { thoughtEmittedResolve = r })
  acp.onPrompt = async (sessionId, _blocks, client) => {
    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'thinking step 1 ' } } })
    client.emit('update', { sessionId, update: { sessionUpdate: 'agent_thought_chunk', content: { type: 'text', text: 'step 2' } } })
    thoughtEmittedResolve()
    return new Promise((r) => { releaseThoughtPrompt = r })
  }
  t.after(() => releaseThoughtPrompt?.({ stopReason: 'cancelled' }))

  await n.peer.call('session.watch', { session_id: thoughtSid })
  await n.peer.call('session.send', { session_id: thoughtSid, text: 'close during thought' })
  await thoughtEmitted
  assert.ok(thoughtState.activeThoughtItem, 'activeThoughtItem must be active before close')

  let streamEndAfterClose = 0
  const origStreamEnd = companion.runtime.streamEnd
  companion.runtime.streamEnd = (sessionId) => {
    if (sessionId === thoughtSid && closedTrackingId === thoughtSid) streamEndAfterClose++
    return origStreamEnd(sessionId)
  }
  t.after(() => { companion.runtime.streamEnd = origStreamEnd })

  await n.peer.call('session.close', { session_id: thoughtSid })
  const thoughtItemsAtClose = thoughtState.liveItems.length
  closedTrackingId = thoughtSid
  appendsAfterClose = 0
  streamEndAfterClose = 0
  releaseThoughtPrompt({ stopReason: 'cancelled' })
  await thoughtState.tail.catch(() => {})
  assert.equal(thoughtState.running, false)
  assert.equal(appendsAfterClose, 0, 'flushActiveThought/pushLiveUpdate must not call runtime.append after session.close')
  assert.equal(streamEndAfterClose, 0, 'finally must not call runtime.streamEnd after session.close')
  assert.equal(thoughtState.liveItems.length, thoughtItemsAtClose, 'liveItems must not grow after session.close')
})

test('assistant pre-prompt interrupt/close guards: ensureGateRules, mcpProxy.allocate, ensureLoaded applySessionConfig, and processFailure', async (t) => {
  // 1) Interrupt while turnDone awaits ensureGateRules() on a queued assistant turn:
  //    must append "已中断" and skip ensureLoaded and acp.prompt.
  {
    const n = await startFakeNode(t)
    const db = fakeV1Db()
    db.session('ses_asst_gate', { directory: '/home/u/.persona/assistant', title: 'Asst Gate' })
    const acp = new FakeAcpClient()
    const companion = createTestCompanion({
      socketPath: n.path,
      retryMs: 20,
      pushDelayMs: 5,
      home: '/home/u',
      acpIdleMs: 0,
      openDb: db.openDb,
      acp,
      discoverModels: async () => [
        { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' },
      ],
    })
    t.after(() => companion.stop())

    // Intercept companion.runtime.tools BEFORE start:
    //  call #1 (onHub refresh): reject so gateShellRulesReady stays false
    //  call #2 (Turn 1 pre-enqueue L1488): pause until Turn 2 queues (wasBusy=true -> skips L1488), then reject Turn 1
    //  call #3 (Turn 2 inside turnDone L1553 ensureGateRules): pause so interrupt hits L1553 directly!
    let toolCallCount = 0
    let turn1ToolsEnteredResolve
    const turn1ToolsEntered = new Promise((r) => { turn1ToolsEnteredResolve = r })
    let rejectTurn1Tools
    let turn2ToolsEnteredResolve
    const turn2ToolsEntered = new Promise((r) => { turn2ToolsEnteredResolve = r })
    let releaseTurn2Tools
    let hubRefreshDoneResolve
    const hubRefreshDone = new Promise((r) => { hubRefreshDoneResolve = r })
    const origTools = companion.runtime.tools
    companion.runtime.tools = async (opts) => {
      toolCallCount++
      if (toolCallCount === 1) {
        hubRefreshDoneResolve()
        throw new Error('initial hub tools unavailable')
      }
      if (toolCallCount === 2) {
        turn1ToolsEnteredResolve()
        await new Promise((_res, rej) => { rejectTurn1Tools = rej })
      }
      if (toolCallCount === 3) {
        turn2ToolsEnteredResolve()
        await new Promise((r) => { releaseTurn2Tools = r })
        return origTools(opts)
      }
      return origTools(opts)
    }
    t.after(() => {
      rejectTurn1Tools?.(new Error('cleanup'))
      releaseTurn2Tools?.()
    })

    await companion.start()
    await n.ready
    await hubRefreshDone

    const turn1Promise = n.peer.call('session.send', { session_id: 'ses_asst_gate', text: 'turn 1 fails upfront', assistant: true })
    await turn1ToolsEntered
    const turn2Queued = await n.peer.call('session.send', { session_id: 'ses_asst_gate', text: 'turn 2 cancelled in ensureGateRules', assistant: true })
    assert.equal(turn2Queued.queued, true)
    rejectTurn1Tools(new Error('turn 1 gate fetch failed'))
    await assert.rejects(turn1Promise)

    await turn2ToolsEntered
    acp.calls.length = 0
    const intrPromise = n.peer.call('session.interrupt', { session_id: 'ses_asst_gate' })
    await until('interrupt registered on session', () => companion.live.get('ses_asst_gate')?.abort?.signal.aborted)
    releaseTurn2Tools()
    const intr = await intrPromise
    assert.equal(intr.settled, true)
    assert.equal(acp.calls.filter((c) => c[0] === 'ensureStarted').length, 0, 'ensureLoaded must not run after interrupt during ensureGateRules')
    assert.equal(acp.calls.filter((c) => c[0] === 'prompt').length, 0, 'acp.prompt must not run after interrupt during ensureGateRules')
    const items = await n.peer.call('session.items', { session_id: 'ses_asst_gate' })
    assert.ok(items.items.some((it) => it.kind === 'notice' && it.text === '已中断'))
  }

  // 2) Close while mcpProxy.allocate is pending inside ensureLoaded():
  //    must release the allocated route immediately and skip resumeSession/prompt.
  {
    const n = await startFakeNode(t)
    const db = fakeV1Db()
    const acp = new FakeAcpClient()
    let allocCalls = 0
    let pauseNextAlloc = false
    let allocEnteredResolve
    const allocEntered = new Promise((r) => { allocEnteredResolve = r })
    let releaseAlloc
    let pausedRouteReleased = 0
    const fakeMcpProxy = {
      async allocate() {
        allocCalls++
        const route = {
          acpServer: { type: 'http', name: 'persona', url: `http://127.0.0.1:9999/mcp/${allocCalls}`, headers: [] },
          bind() {},
          release() {},
        }
        if (pauseNextAlloc) {
          pauseNextAlloc = false
          route.release = () => { pausedRouteReleased++ }
          allocEnteredResolve()
          await new Promise((r) => { releaseAlloc = r })
        }
        return route
      },
      async forSession() {
        return { acpServer: { type: 'http', name: 'persona', url: 'http://127.0.0.1:9999/mcp/s', headers: [] }, release() {} }
      },
      stop() {},
    }
    t.after(() => releaseAlloc?.())

    const companion = createTestCompanion({
      socketPath: n.path,
      retryMs: 20,
      pushDelayMs: 5,
      home: '/home/u',
      acpIdleMs: 0,
      openDb: db.openDb,
      acp,
      mcpProxy: fakeMcpProxy,
      discoverModels: async () => [
        { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' },
      ],
    })
    t.after(() => companion.stop())
    await companion.start()
    await n.ready

    const started = await n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true })
    const sid = started.session.session_id
    const s = companion.live.get(sid)
    // Simulate a session whose worker exists but needs ensureLoaded to allocate a fresh mcpRoute.
    s.mcpRoute = null
    s.loadedGeneration = 0
    pauseNextAlloc = true
    acp.calls.length = 0

    await n.peer.call('session.send', { session_id: sid, text: 'close during allocate', assistant: true })
    await allocEntered
    await n.peer.call('session.close', { session_id: sid })
    releaseAlloc()
    await s.tail.catch(() => {})
    assert.equal(pausedRouteReleased, 1, 'allocated mcpRoute must be released when session closed during allocate')
    assert.equal(acp.calls.filter((c) => c[0] === 'resumeSession').length, 0)
    assert.equal(acp.calls.filter((c) => c[0] === 'prompt').length, 0)
  }

  // 3) Close during assistant ensureLoaded()'s applySessionConfig():
  //    must throw closed, never leak a non-zero runtime.assistantPid after close, and never call prompt.
  // 4) Process exit (processFailure) while paused in assistant ensureLoaded()'s applySessionConfig():
  //    must record "出错了：OpenCode execution process exited unexpectedly" (NOT "已中断").
  {
    const n = await startFakeNode(t)
    const db = fakeV1Db()
    const acp = new FakeAcpClient()
    const workersBySession = new Map()
    const companion = createOpencodeCompanion({
      socketPath: n.path,
      retryMs: 20,
      pushDelayMs: 5,
      home: '/home/u',
      acpIdleMs: 0,
      openDb: db.openDb,
      acp,
      discoverModels: async () => [
        { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' },
        { providerID: 'openai', id: 'gpt-5', name: 'GPT-5' },
      ],
      assistantAcpFactory: () => {
        const child = new FakeAcpClient()
        child.calls = acp.calls
        Object.defineProperty(child, 'nextId', { get: () => acp.nextId, set: (v) => { acp.nextId = v } })
        const origNew = child.newSession.bind(child)
        child.newSession = async (params) => {
          const res = await origNew(params)
          workersBySession.set(res.sessionId, child)
          return res
        }
        return child
      },
    })
    t.after(() => companion.stop())
    await companion.start()
    await n.ready

    // Case 3: close during assistant ensureLoaded -> applySessionConfig
    const started1 = await n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true, model: 'openai/gpt-5' })
    const sid1 = started1.session.session_id
    const s1 = companion.live.get(sid1)
    const workerAcp1 = workersBySession.get(sid1)
    s1.loadedGeneration = 0
    workerAcp1.loadedSessions.delete(sid1)
    let releaseAsstConfig1
    let asstConfig1EnteredResolve
    const asstConfig1Entered = new Promise((r) => { asstConfig1EnteredResolve = r })
    const origSetConfig1 = workerAcp1.setConfigOption.bind(workerAcp1)
    workerAcp1.setConfigOption = async (sessionId, configId, value) => {
      asstConfig1EnteredResolve()
      await new Promise((r) => { releaseAsstConfig1 = r })
      return origSetConfig1(sessionId, configId, value)
    }
    t.after(() => releaseAsstConfig1?.())

    const pidCallsAfterClose = []
    let trackClosePidFor = ''
    const origAssistantPid = companion.runtime.assistantPid
    companion.runtime.assistantPid = (sessionId, pid) => {
      if (sessionId === trackClosePidFor) pidCallsAfterClose.push(pid)
      return origAssistantPid(sessionId, pid)
    }
    t.after(() => { companion.runtime.assistantPid = origAssistantPid })

    acp.calls.length = 0
    await n.peer.call('session.send', { session_id: sid1, text: 'close during asst applySessionConfig', assistant: true })
    await asstConfig1Entered
    await n.peer.call('session.close', { session_id: sid1 })
    trackClosePidFor = sid1
    pidCallsAfterClose.length = 0
    releaseAsstConfig1()
    await s1.tail.catch(() => {})
    assert.deepEqual(pidCallsAfterClose, [], 'must not leak runtime.assistantPid after close during ensureLoaded applySessionConfig')
    assert.equal(s1.loadedGeneration, 0)
    assert.equal(acp.calls.filter((c) => c[0] === 'prompt' && c[1].sessionId === sid1).length, 0)

    // Case 4: worker process crash (processFailure) during pre-prompt ensureLoaded -> applySessionConfig
    const started2 = await n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true, model: 'openai/gpt-5' })
    const sid2 = started2.session.session_id
    const s2 = companion.live.get(sid2)
    const workerAcp2 = workersBySession.get(sid2)
    s2.loadedGeneration = 0
    workerAcp2.loadedSessions.delete(sid2)
    let releaseAsstConfig2
    let asstConfig2EnteredResolve
    const asstConfig2Entered = new Promise((r) => { asstConfig2EnteredResolve = r })
    const origSetConfig2 = workerAcp2.setConfigOption.bind(workerAcp2)
    workerAcp2.setConfigOption = async (sessionId, configId, value) => {
      asstConfig2EnteredResolve()
      await new Promise((r) => { releaseAsstConfig2 = r })
      return origSetConfig2(sessionId, configId, value)
    }
    t.after(() => releaseAsstConfig2?.())

    acp.calls.length = 0
    await n.peer.call('session.send', { session_id: sid2, text: 'crash during pre-prompt', assistant: true })
    await asstConfig2Entered
    workerAcp2.emit('process', 0)
    releaseAsstConfig2()
    await s2.tail.catch(() => {})
    assert.equal(acp.calls.filter((c) => c[0] === 'prompt' && c[1].sessionId === sid2).length, 0)
    assert.equal(s2.model, 'openai/gpt-5', 'desired assistant model must be preserved after pre-prompt abort')
    const crashItems = await n.peer.call('session.items', { session_id: sid2 })
    assert.equal(
      crashItems.items.at(-1)?.text,
      '出错了：OpenCode execution process exited unexpectedly',
      'processFailure during pre-prompt cancel must report error notice rather than 已中断',
    )
  }

  // 5) Two concurrent ensureLoaded() calls on a non-assistant session sharing the same mcpProxy.forSession path
  //    must NOT release/delete the live MCP route!
  {
    const n = await startFakeNode(t)
    const db = fakeV1Db()
    const acp = new FakeAcpClient()
    let forSessionCalls = 0
    const releases = []
    let firstForSessionEnteredResolve
    const firstForSessionEntered = new Promise((r) => { firstForSessionEnteredResolve = r })
    let secondForSessionEnteredResolve
    const secondForSessionEntered = new Promise((r) => { secondForSessionEnteredResolve = r })
    let releaseFirst
    let releaseSecond
    const sharedPath = '/mcp/session/shared_concurrent'
    const fakeMcpProxy = {
      async allocate() {
        return {
          path: '/mcp/alloc_1',
          acpServer: { type: 'http', name: 'persona', url: 'http://127.0.0.1:9999/mcp/alloc_1', headers: [] },
          bind() {},
          release() {},
        }
      },
      async forSession() {
        const callNum = ++forSessionCalls
        const route = {
          path: sharedPath,
          acpServer: { type: 'http', name: 'persona', url: `http://127.0.0.1:9999${sharedPath}`, headers: [] },
          release() { releases.push(callNum) },
        }
        if (callNum === 1) {
          firstForSessionEnteredResolve()
          await new Promise((r) => { releaseFirst = r })
        } else if (callNum === 2) {
          secondForSessionEnteredResolve()
          await new Promise((r) => { releaseSecond = r })
        }
        return route
      },
      stop() {},
    }
    t.after(() => { releaseFirst?.(); releaseSecond?.() })

    const companion = createTestCompanion({
      socketPath: n.path,
      retryMs: 20,
      pushDelayMs: 5,
      home: '/home/u',
      acpIdleMs: 0,
      openDb: db.openDb,
      acp,
      mcpProxy: fakeMcpProxy,
      discoverModels: async () => [
        { providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' },
        { providerID: 'openai', id: 'gpt-5', name: 'GPT-5' },
      ],
    })
    t.after(() => companion.stop())
    await companion.start()
    await n.ready

    const started = await n.peer.call('session.start', { cwd: '/w/app' })
    const sid = started.session.session_id
    const s = companion.live.get(sid)
    s.mcpRoute = null
    s.loadedGeneration = 0
    acp.loadedSessions.delete(sid)

    // Launch send() and modes() concurrently so both enter ensureLoaded() -> mcpProxy.forSession(s).
    await n.peer.call('session.send', { session_id: sid, text: 'concurrent route check' })
    await firstForSessionEntered
    const modesPromise = n.peer.call('session.modes', { session_id: sid, model: 'openai/gpt-5' })
    await secondForSessionEntered
    releaseFirst()
    releaseSecond()
    await modesPromise
    await s.tail
    assert.equal(forSessionCalls, 2)
    assert.deepEqual(releases, [], 'concurrent forSession() calls sharing the same deterministic path must never release/delete the live route')
    assert.equal(s.mcpRoute?.path, sharedPath)
  }
})

test('companion assistant isMemoryWrite blocks symlink escapes, namei divergence, missing cwd fallback, conflicting keys, and clipInput truncation', async (t) => {
  const base = mkdtempSync(join(tmpdir(), 'persona-oc-comp-mem-'))
  t.after(() => rmSync(base, { recursive: true, force: true }))
  const asstDir = join(base, 'assistant')
  const memDir = join(asstDir, 'memory')
  const nestedMem = join(memDir, 'nested', 'deep')
  const outsideDir = join(base, 'outside')
  const outsideSub = join(outsideDir, 'sub')
  mkdirSync(nestedMem, { recursive: true })
  mkdirSync(outsideSub, { recursive: true })
  writeFileSync(join(outsideDir, 'secret.txt'), 'secret')
  const escapeFileLink = join(memDir, 'escape-file.md')
  const danglingLink = join(memDir, 'dangling.md')
  const escapeDirLink = join(memDir, 'out-dir')
  const deepLink = join(memDir, 'deep-link')
  const loopA = join(memDir, 'loop-a')
  const loopB = join(memDir, 'loop-b')
  symlinkSync(join(outsideDir, 'secret.txt'), escapeFileLink)
  symlinkSync(join(outsideDir, 'new-evil.txt'), danglingLink)
  symlinkSync(outsideSub, escapeDirLink)
  symlinkSync(nestedMem, deepLink)
  symlinkSync(loopB, loopA)
  symlinkSync(loopA, loopB)

  const n = await startFakeNode(t, { tools: [] })
  n.resolve = () => ({ allow: false })
  const db = fakeV1Db()
  const acp = new FakeAcpClient()
  const companion = createTestCompanion({
    socketPath: n.path,
    retryMs: 20,
    pushDelayMs: 5,
    home: base,
    openDb: db.openDb,
    acp,
    discoverModels: async () => [{ providerID: 'opencode-go', id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' }],
  })
  t.after(() => companion.stop())
  await companion.start()
  await n.ready

  const started = await n.peer.call('session.start', { cwd: asstDir, assistant: true })
  const sid = started.session.session_id
  const s = companion.live.get(sid)
  const opts = [
    { optionId: 'once', kind: 'allow_once', name: 'Allow' },
    { optionId: 'reject', kind: 'reject_once', name: 'Reject' },
  ]
  const paddedEscape = `${memDir}/${'./'.repeat(6300)}../SOUL.md`
  const sparseLocations = [{ path: join(memDir, 'ok.md') }]
  sparseLocations.length = 2
  const deniedCases = [
    { id: 'sym_file', rawInput: { filePath: escapeFileLink } },
    { id: 'sym_dangling', rawInput: { filePath: danglingLink } },
    { id: 'sym_dir', rawInput: { filePath: join(escapeDirLink, 'pwn.txt') } },
    { id: 'namei_dotdot', rawInput: { filePath: `${escapeDirLink}/../secret.txt` } },
    { id: 'lex_dotdot', rawInput: { filePath: `${deepLink}/../../SOUL.md` } },
    { id: 'sym_loop', rawInput: { filePath: loopA } },
    { id: 'tilde', rawInput: { filePath: '~/SOUL.md' } },
    { id: 'nul', rawInput: { filePath: `${join(memDir, 'ok.md')}\0` } },
    { id: 'mem_dir_self', rawInput: { filePath: memDir } },
    { id: 'conflict_path', rawInput: { filePath: join(memDir, 'ok.md'), path: join(outsideDir, 'secret.txt') } },
    { id: 'conflict_file_path', rawInput: { filePath: join(memDir, 'ok.md'), file_path: join(outsideDir, 'secret.txt') } },
    { id: 'non_string_key', rawInput: { filePath: 123, path: join(memDir, 'ok.md') } },
    { id: 'unknown_filepath_lower', rawInput: { filePath: join(memDir, 'ok.md'), filepath: join(outsideDir, 'secret.txt') } },
    { id: 'unknown_newPath', rawInput: { filePath: join(memDir, 'ok.md'), newPath: join(outsideDir, 'secret.txt') } },
    { id: 'unknown_diff', rawInput: { filePath: join(memDir, 'ok.md'), diff: '--- a/etc/hosts' } },
    { id: 'edits_array_item', rawInput: { filePath: join(memDir, 'ok.md'), edits: [[]] } },
    { id: 'edits_unknown_filepath', rawInput: { filePath: join(memDir, 'ok.md'), edits: [{ filepath: join(outsideDir, 'secret.txt') }] } },
    { id: 'edits_unknown_patchText', rawInput: { filePath: join(memDir, 'ok.md'), edits: [{ patchText: '*** Update File: /etc/hosts' }] } },
    { id: 'title_apply_patch', title: 'apply_patch', rawInput: { filePath: join(memDir, 'ok.md') } },
    { id: 'title_move_file', title: 'move_file', rawInput: { filePath: join(memDir, 'ok.md') } },
    { id: 'title_delete', title: 'delete', rawInput: { filePath: join(memDir, 'ok.md') } },
    { id: 'patch_text', rawInput: { filePath: join(memDir, 'ok.md'), patchText: '*** Update File: /etc/hosts' } },
    { id: 'files_array', rawInput: { filePath: join(memDir, 'ok.md'), files: [join(outsideDir, 'secret.txt')] } },
    { id: 'locations_mismatch', rawInput: { filePath: join(memDir, 'ok.md') }, locations: [{ path: join(outsideDir, 'secret.txt') }] },
    { id: 'locations_sparse', rawInput: { filePath: join(memDir, 'ok.md') }, locations: sparseLocations },
    { id: 'content_diff_outside', rawInput: { filePath: join(memDir, 'ok.md') }, content: [{ type: 'diff', path: join(outsideDir, 'secret.txt'), oldText: 'a', newText: 'b' }] },
    { id: 'content_diff_missing_path', rawInput: { filePath: join(memDir, 'ok.md') }, content: [{ type: 'diff', oldText: 'a', newText: 'b' }] },
    { id: 'content_non_array', rawInput: { filePath: join(memDir, 'ok.md') }, content: { type: 'diff', path: join(memDir, 'ok.md') } },
    { id: 'clip_escape', rawInput: { filePath: paddedEscape, content: 'x'.repeat(20_000) } },
  ]

  let promptRan = false
  let promptErr = null
  acp.onPrompt = async (sessionId, _blocks, client) => {
    promptRan = true
    try {
      for (const tc of deniedCases) {
        const before = n.asked.length
        const res = await client.onPermissionRequest({
          sessionId,
          toolCall: {
            toolCallId: tc.id,
            kind: 'edit',
            title: tc.title ?? 'write',
            rawInput: tc.rawInput,
            ...(tc.locations !== undefined ? { locations: tc.locations } : {}),
            ...(tc.content !== undefined ? { content: tc.content } : {}),
          },
          options: opts,
        })
        assert.deepEqual(res, { outcome: { outcome: 'selected', optionId: 'reject' } }, `${tc.id} must not auto-allow`)
        assert.equal(n.asked.length, before + 1, `${tc.id} must ask phone`)
      }

      // Empty cwd must NOT fall back to memoryDir for relative paths.
      const savedCwd = s.cwd
      s.cwd = ''
      try {
        const before = n.asked.length
        const res = await client.onPermissionRequest({
          sessionId,
          toolCall: { toolCallId: 'empty_cwd', kind: 'edit', title: 'write', rawInput: { filePath: 'AGENTS.md' } },
          options: opts,
        })
        assert.deepEqual(res, { outcome: { outcome: 'selected', optionId: 'reject' } }, 'empty cwd must not fall back to memoryDir')
        assert.equal(n.asked.length, before + 1)
      } finally {
        s.cwd = savedCwd
      }

      // Root filesystem memoryDir must fail closed.
      const savedMem = s.memoryDir
      s.memoryDir = '/'
      try {
        const before = n.asked.length
        const res = await client.onPermissionRequest({
          sessionId,
          toolCall: { toolCallId: 'root_mem', kind: 'edit', title: 'write', rawInput: { filePath: '/etc/hosts' } },
          options: opts,
        })
        assert.deepEqual(res, { outcome: { outcome: 'selected', optionId: 'reject' } }, 'root memoryDir must fail closed')
        assert.equal(n.asked.length, before + 1)
      } finally {
        s.memoryDir = savedMem
      }

      // Legitimate writes inside memoryDir still auto-allow without asking phone.
      const askedBeforeAllowed = n.asked.length
      for (const tc of [
        { id: 'ok_abs', rawInput: { filePath: join(memDir, 'notes.md') } },
        { id: 'ok_rel', rawInput: { filePath: 'memory/sub/new.md' } },
        { id: 'ok_dotdot_name', rawInput: { filePath: join(memDir, '..note.md') } },
        { id: 'ok_loc', rawInput: { filePath: join(memDir, 'notes.md') }, locations: [{ path: join(memDir, 'notes.md') }] },
        { id: 'ok_content_diff', rawInput: { filePath: join(memDir, 'notes.md') }, content: [{ type: 'diff', path: join(memDir, 'notes.md'), oldText: 'a', newText: 'b' }] },
      ]) {
        const res = await client.onPermissionRequest({
          sessionId,
          toolCall: {
            toolCallId: tc.id,
            kind: 'edit',
            title: 'write',
            rawInput: tc.rawInput,
            ...(tc.locations !== undefined ? { locations: tc.locations } : {}),
            ...(tc.content !== undefined ? { content: tc.content } : {}),
          },
          options: opts,
        })
        assert.deepEqual(res, { outcome: { outcome: 'selected', optionId: 'once' } }, `${tc.id} must auto-allow`)
      }
      assert.equal(n.asked.length, askedBeforeAllowed, 'legitimate memory writes must not ask phone')
    } catch (err) {
      promptErr = err
      throw err
    }

    return { stopReason: 'end_turn' }
  }

  await n.peer.call('session.send', {
    session_id: sid,
    text: 'memory security check',
    assistant: true,
    mode: 'default',
    memory_dir: memDir,
    client_msg_id: 'cm_mem_sec_1',
  })
  await s.tail
  assert.equal(promptRan, true, 'acp.onPrompt must have run')
  if (promptErr) throw promptErr
  assert.equal(s.turnError, '')
})
