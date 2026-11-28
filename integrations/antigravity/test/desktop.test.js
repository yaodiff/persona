import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createServer } from 'node:http'
import { appendFileSync, chmodSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until as waitUntil } from '../../runtime-sdk/testing/fake-node.js'
import { conversationRpc, createDesktopRuntime, extractModelEntries, resolveModel, rpc } from '../lib/desktop.js'
import { cleanUserText } from '../lib/map.js'

const DESKTOP_ENTRY = fileURLToPath(new URL('../desktop.mjs', import.meta.url))

test('desktop attaches to existing sessions, follows transcripts, and routes only their controls', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'desktop-agy-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const key = '11111111-2222-4333-8444-555555555555'
  const session_id = 'desktop-agy-' + key
  const folder = join(dir, 'brain', key, '.system_generated', 'logs')
  mkdirSync(folder, { recursive: true })
  const file = join(folder, 'transcript.jsonl')
  writeFileSync(file, JSON.stringify({ step_index: 0, type: 'USER_INPUT', content: 'hi' }) + '\n')
  const node = await startFakeNode(t)
  const calls = []
  const cascadeConfig = { plannerConfig: { planModel: "test-model" } }
  const runtime = createDesktopRuntime({ dir, socketPath: node.path, discover: async () => ({}),
    call: async (_, method, params) => {
      if (method === 'GetAllCascadeTrajectories') return { trajectorySummaries: { [key]: {
        summary: 'test session', lastModifiedTime: new Date().toISOString(), stepCount: 1,
      } } }
      if (method === 'GetCascadeTrajectorySteps') return { steps: [{ userInput: { userConfig: cascadeConfig } }] }
      calls.push({ method, params })
      return {}
    } })
  t.after(() => runtime.stop())
  await runtime.start()
  await node.ready
  const until = async fn => {
    for (let i = 0; i < 100 && !fn(); i++) await delay(25)
    assert.ok(fn())
  }
  await until(() => node.last('runtime.sessions'))
  assert.equal(node.last('runtime.sessions').sessions[0].session_id, session_id)
  const read = await node.peer.call('session.items', { session_id })
  assert.equal(read.items[0].text, 'hi')
  await node.peer.call('session.watch', { session_id })
  appendFileSync(file, [
    { step_index: 1, type: 'PLANNER_RESPONSE', content: 'hello' },
    { step_index: 2, type: 'GENERIC', content: 'captured', media: [{ mime_type: 'image/png', uri: 'capture.png' }] },
  ].map((step) => JSON.stringify(step)).join('\n') + '\n')
  await until(() => node.last('session.items'))
  assert.deepEqual(node.last('session.items').items.map((item) => item.kind), ['text', 'tool_result'])
  assert.deepEqual((await node.peer.call('session.items', { session_id })).items.map((item) => item.kind), ['text', 'text', 'tool_result'])
  await node.peer.call('session.send', { session_id, text: 'next', client_msg_id: 'one' })
  await node.peer.call('session.send', { session_id, text: 'next', client_msg_id: 'one' })
  await node.peer.call('session.interrupt', { session_id })
  await assert.rejects(node.peer.call('session.send', { session_id: 'agy-' + key, text: 'wrong' }), { code: 'not_found' })
  assert.deepEqual(calls, [
    { method: 'SendUserCascadeMessage', params: { cascadeId: key, items: [{ text: 'next' }], cascadeConfig, clientMessageId: 'one' } },
    { method: 'CancelCascadeInvocation', params: { cascadeId: key } },
  ])
})

test('desktop creates new conversations via ConversationService, selects models, and sends follow-ups', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'desktop-agy-start-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const createdId = '22222222-3333-4444-8555-666666666666'
  const emptyId = '33333333-4444-4555-8666-777777777777'
  const convCalls = []
  const lsCalls = []
  const conversations = new Map()
  const node = await startFakeNode(t)

  const runtime = createDesktopRuntime({
    dir,
    home: '/workspace/home',
    socketPath: node.path,
    workingPollMs: 0,
    discover: async () => ({ httpPort: 12345, csrfToken: 'tok' }),
    conversationCall: async (_, method, params) => {
      convCalls.push({ method, params })
      if (method === 'ListModels') {
        return {
          models: [
            { id: 'gemini-4-neon-high', displayName: 'Gemini 4 Neon (High)', default: true },
            { id: 'gemini-4-neon-low', displayName: 'Gemini 4 Neon (Low)' },
            { id: 'pro', displayName: 'Antigravity Pro (Auto)' },
          ],
        }
      }
      if (method === 'CreateConversation') {
        const id = params.prompt?.length ? createdId : emptyId
        const conv = {
          id,
          metadata: { createTime: '2026-11-25T10:00:00.000Z', tags: params.tags || [] },
          annotations: params.title ? { title: params.title } : {},
          state: { runState: params.prompt?.length ? 'RUN_STATE_RUNNING' : 'RUN_STATE_IDLE', updateTime: '2026-11-25T10:00:00.000Z' },
        }
        conversations.set(id, conv)
        return { conversation: conv }
      }
      if (method === 'ListConversations') {
        return { conversations: [...conversations.values()].filter(c => !c.annotations?.archived) }
      }
      if (method === 'GetConversation') {
        const found = conversations.get(params.conversationId)
        if (!found) throw new Error('not found')
        return { conversation: found }
      }
      return {}
    },
    call: async (_, method, params) => {
      lsCalls.push({ method, params })
      if (method === 'GetAllCascadeTrajectories') return { trajectorySummaries: {} }
      return {}
    },
  })
  t.after(() => runtime.stop())
  await runtime.start()
  await node.ready

  assert.equal(node.hello.runtime, 'antigravity-desktop')
  assert.equal(node.hello.launcher, 'antigravity-desktop')
  assert.equal(node.hello.caps.start, true)
  assert.equal(node.hello.caps.models, true)
  assert.equal(node.hello.caps.rename, true)
  assert.equal(node.hello.caps.archive, true)

  const catalog = await node.peer.call('launchers.models', { launcher: 'antigravity-desktop', refresh: true })
  assert.equal(catalog.agent, 'antigravity-desktop')
  assert.deepEqual(catalog.models, [
    { id: 'gemini-4-neon-high', name: 'Gemini 4 Neon (High)', default: true, efforts: ['low', 'high'], default_effort: 'high' },
    { id: 'gemini-4-neon-low', name: 'Gemini 4 Neon (Low)', default: false, efforts: ['low', 'high'], default_effort: 'low' },
    { id: 'pro', name: 'Antigravity Pro (Auto)', default: false, efforts: [] },
  ])
  assert.equal(node.last('runtime.launchers').launchers[0].model, 'gemini-4-neon-high')

  // Start with prompt, workspace cwd, context, explicit model + effort override, and client_msg_id.
  const started = await node.peer.call('session.start', {
    cwd: '/workspace/project-a',
    prompt: 'Summarize the changes',
    context: 'Branch: main',
    model: 'Gemini 4 Neon (High)',
    effort: 'low',
    title: 'Desktop review',
    client_msg_id: 'start-msg-1',
  })
  assert.equal(started.session.session_id, 'desktop-agy-' + createdId)
  assert.equal(started.session.model, 'gemini-4-neon-low')
  assert.equal(started.session.status, 'working')
  assert.equal(started.session.cwd, '/workspace/project-a')
  assert.equal(started.session.started_by_persona, true)
  assert.equal(started.session.launcher, 'antigravity-desktop')

  const createCall = convCalls.find(c => c.method === 'CreateConversation')
  assert.equal(createCall.params.model, 'gemini-4-neon-low')
  assert.equal(createCall.params.title, 'Desktop review')
  assert.deepEqual(createCall.params.tags, ['persona'])
  assert.match(createCall.params.prompt[0].text, /Working directory: \/workspace\/project-a/)
  assert.match(createCall.params.prompt[0].text, /Branch: main/)
  assert.match(createCall.params.prompt[0].text, /Summarize the changes$/)

  // Verify exported transcript attaches recorded client_msg_id and strips context blocks.
  const logsDir = join(dir, 'brain', createdId, '.system_generated', 'logs')
  mkdirSync(logsDir, { recursive: true })
  writeFileSync(join(logsDir, 'transcript.jsonl'), JSON.stringify({
    step_index: 0,
    type: 'USER_INPUT',
    source: 'USER_EXPLICIT',
    content: createCall.params.prompt[0].text,
  }) + '\n')
  const items = await node.peer.call('session.items', { session_id: started.session.session_id })
  assert.equal(items.items[0].text, 'Summarize the changes')
  assert.equal(items.items[0].client_msg_id, 'start-msg-1')

  // Start idle session without prompt and verify follow-up send works without cascadeConfig.
  const idle = await node.peer.call('session.start', { cwd: '/workspace/home', model: 'pro', effort: 'high' })
  assert.equal(idle.session.session_id, 'desktop-agy-' + emptyId)
  assert.equal(idle.session.model, 'pro')
  assert.equal(idle.session.status, 'idle')
  const emptyCreate = convCalls.filter(c => c.method === 'CreateConversation')[1]
  assert.equal(emptyCreate.params.prompt, undefined)
  assert.equal(emptyCreate.params.model, 'pro')

  await node.peer.call('session.send', { session_id: idle.session.session_id, text: 'First follow-up', client_msg_id: 'follow-1' })
  const sendCall = lsCalls.find(c => c.method === 'SendUserCascadeMessage')
  assert.deepEqual(sendCall.params, {
    cascadeId: emptyId,
    items: [{ text: 'First follow-up' }],
    clientMessageId: 'follow-1',
  })

  // Image prompts remain unsupported on start.
  await assert.rejects(
    node.peer.call('session.start', { cwd: '/workspace/home', images: [{ media_type: 'image/png', data: 'abc' }] }),
    { code: 'unsupported' },
  )
})

test('desktop supports rename, reversible archive/unarchive, settled interrupt, and probe filtering', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'desktop-agy-life-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const activeId = '44444444-5555-4666-8777-888888888888'
  const probeId = '55555555-6666-4777-8888-999999999999'
  const sessionId = 'desktop-agy-' + activeId
  const convState = new Map([
    [activeId, {
      id: activeId,
      metadata: { createTime: new Date().toISOString(), tags: ['persona'] },
      annotations: { title: 'Initial Title', archived: false },
      state: { runState: 'RUN_STATE_RUNNING', updateTime: new Date().toISOString() },
    }],
    [probeId, {
      id: probeId,
      metadata: { createTime: new Date().toISOString(), tags: ['persona-probe'] },
      annotations: { title: 'Probe' },
      state: { runState: 'RUN_STATE_IDLE', updateTime: new Date().toISOString() },
    }],
  ])
  let cancelCount = 0
  let getPollsAfterCancel = 0
  const updates = []
  const node = await startFakeNode(t)

  const runtime = createDesktopRuntime({
    dir,
    socketPath: node.path,
    workingPollMs: 0,
    interruptPollMs: 10,
    interruptTimeoutMs: 500,
    discover: async () => ({ httpPort: 12345, csrfToken: 'tok' }),
    conversationCall: async (_, method, params) => {
      if (method === 'ListConversations') {
        return { conversations: [...convState.values()].filter(c => !c.annotations?.archived) }
      }
      if (method === 'GetConversation') {
        const entry = convState.get(params.conversationId)
        if (!entry) throw new Error('missing')
        if (cancelCount > 0 && ++getPollsAfterCancel >= 2) entry.state.runState = 'RUN_STATE_IDLE'
        // Exercise top-level { state } response shape (in addition to { conversation }).
        return { state: entry.state }
      }
      if (method === 'UpdateConversation') {
        updates.push(params)
        const entry = convState.get(params.conversationId)
        entry.annotations = { ...entry.annotations, ...params.annotations }
        return {}
      }
      return {}
    },
    call: async (_, method) => {
      if (method === 'GetAllCascadeTrajectories') return { trajectorySummaries: {} }
      if (method === 'CancelCascadeInvocation') {
        cancelCount++
        return {}
      }
      return {}
    },
  })
  t.after(() => runtime.stop())
  await runtime.start()
  await node.ready

  await waitUntil('active session listed without probe', () => {
    const list = node.last('runtime.sessions')?.sessions
    return list?.length === 1 && list[0].session_id === sessionId && list[0].title === 'Initial Title' && list[0].status === 'working'
  })

  // Interrupt settles once GetConversation reports RUN_STATE_IDLE.
  const interrupted = await node.peer.call('session.interrupt', { session_id: sessionId })
  assert.deepEqual(interrupted, { ok: true, settled: true })
  assert.equal(cancelCount, 1)

  // Rename updates annotations and reflected session title.
  await node.peer.call('session.rename', { session_id: sessionId, title: 'Renamed Session' })
  await waitUntil('renamed title pushed', () => node.last('runtime.sessions')?.sessions?.[0]?.title === 'Renamed Session')

  // Archive hides the conversation cleanly; unarchive restores it.
  await node.peer.call('session.archive', { session_id: sessionId })
  await waitUntil('archived session removed', () => node.last('runtime.sessions')?.sessions?.length === 0)

  await node.peer.call('session.unarchive', { session_id: sessionId })
  await waitUntil('unarchived session restored', () => node.last('runtime.sessions')?.sessions?.[0]?.session_id === sessionId)

  assert.deepEqual(updates, [
    { conversationId: activeId, annotations: { title: 'Renamed Session' } },
    { conversationId: activeId, annotations: { archived: true } },
    { conversationId: activeId, annotations: { archived: false } },
  ])
})

test('desktop falls back to GetCascadeModelConfigData, settings, transcripts, and aliases cleanly', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'desktop-agy-models-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  writeFileSync(join(dir, 'settings.json'), JSON.stringify({ model: 'gemini-4-neon-high' }))
  const observedId = '66666666-7777-4888-8999-aaaaaaaaaaaa'
  const logsDir = join(dir, 'brain', observedId, '.system_generated', 'logs')
  mkdirSync(logsDir, { recursive: true })
  writeFileSync(join(logsDir, 'transcript.jsonl'), JSON.stringify({
    step_index: 0,
    type: 'PLANNER_RESPONSE',
    status: 'DONE',
    model: 'claude-opus-5-5-high',
    content: 'Done.',
  }) + '\n')

  const node = await startFakeNode(t)
  let configDataResponse = {
    clientModelConfigs: [
      { label: 'Opaque', modelOrAlias: { model: 'MODEL_PLACEHOLDER_M99' } },
    ],
  }
  const runtime = createDesktopRuntime({
    dir,
    socketPath: node.path,
    workingPollMs: 0,
    discover: async () => ({ httpPort: 12345, csrfToken: 'tok' }),
    conversationCall: async (_, method) => {
      if (method === 'ListModels') throw new Error('HTTP 404')
      if (method === 'ListConversations') {
        return {
          conversations: [{
            id: observedId,
            metadata: { createTime: new Date().toISOString() },
            state: { runState: 'RUN_STATE_IDLE', updateTime: new Date().toISOString() },
          }],
        }
      }
      return {}
    },
    call: async (_, method) => {
      if (method === 'GetAllCascadeTrajectories') return { trajectorySummaries: {} }
      if (method === 'GetCascadeModelConfigData') return configDataResponse
      return {}
    },
  })
  t.after(() => runtime.stop())
  await runtime.start()
  await node.ready

  await waitUntil('observed session listed with done status', () =>
    node.last('runtime.sessions')?.sessions?.[0]?.status === 'done' &&
    node.last('runtime.sessions')?.sessions?.[0]?.model === 'claude-opus-5-5-high')

  // Opaque MODEL_PLACEHOLDER_* entries are ignored; fallback uses settings + observed transcript models + auto aliases.
  const fallback = await node.peer.call('launchers.models', { launcher: 'antigravity-desktop', refresh: true })
  assert.deepEqual(fallback.models.map(m => m.id), ['gemini-4-neon-high', 'claude-opus-5-5-high', 'pro', 'flash', 'flash_lite'])
  assert.equal(fallback.models[0].default, true)

  // When GetCascadeModelConfigData exposes real model IDs, discovery uses them and honors its default override.
  configDataResponse = {
    clientModelConfigs: [
      { label: 'Gemini 4 Neon (High)', modelOrAlias: { alias: 'gemini-4-neon-high' } },
      { label: 'Gemini 4 Neon (Medium)', modelOrAlias: { alias: 'gemini-4-neon-medium' } },
    ],
    defaultOverrideModelConfig: { modelOrAlias: { alias: 'gemini-4-neon-medium' } },
  }
  const refreshed = await node.peer.call('launchers.models', { launcher: 'antigravity-desktop', refresh: true })
  assert.deepEqual(refreshed.models, [
    { id: 'gemini-4-neon-high', name: 'Gemini 4 Neon (High)', default: false, efforts: ['medium', 'high'], default_effort: 'high' },
    { id: 'gemini-4-neon-medium', name: 'Gemini 4 Neon (Medium)', default: true, efforts: ['medium', 'high'], default_effort: 'medium' },
  ])
})

test('desktop HTTP RPC clients send expected Connect and LanguageServer headers and retry hub restarts', async t => {
  const seen = []
  const server = createServer((req, res) => {
    const chunks = []
    req.on('data', chunk => chunks.push(chunk))
    req.on('end', () => {
      const body = JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}')
      seen.push({ url: req.url, headers: req.headers, body })
      if (req.url.endsWith('/FailMethod')) {
        res.writeHead(400, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ message: 'invalid request' }))
        return
      }
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ ok: true, method: req.url }))
    })
  })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  t.after(() => server.close())
  const { port } = server.address()
  const hub = { httpPort: port, csrfToken: 'test-csrf-token' }

  const convRes = await conversationRpc(hub, 'CreateConversation', { model: 'pro', tags: ['persona'] })
  assert.equal(convRes.ok, true)
  const lsRes = await rpc(hub, 'GetAllCascadeTrajectories', {})
  assert.equal(lsRes.ok, true)
  await assert.rejects(conversationRpc(hub, 'FailMethod', {}), /HTTP 400: invalid request/)

  assert.equal(seen[0].url, '/product.v1.ConversationService/CreateConversation')
  assert.equal(seen[0].headers['connect-protocol-version'], '1')
  assert.equal(seen[0].headers['x-antigravity-csrf-token'], 'test-csrf-token')
  assert.deepEqual(seen[0].body, { model: 'pro', tags: ['persona'] })
  assert.equal(seen[1].url, '/exa.language_server_pb.LanguageServerService/GetAllCascadeTrajectories')
  assert.equal(seen[1].headers['x-codeium-csrf-token'], 'test-csrf-token')

  // Verify hub rediscovery retry on ECONNREFUSED.
  const dir = mkdtempSync(join(tmpdir(), 'desktop-agy-retry-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const node = await startFakeNode(t)
  let discoverCalls = 0
  const freshId = '77777777-8888-4999-8aaa-bbbbbbbbbbbb'
  const runtime = createDesktopRuntime({
    dir,
    socketPath: node.path,
    hubCacheMs: 60000,
    workingPollMs: 0,
    discover: async () => ({ httpPort: ++discoverCalls === 1 ? 1 : 2, csrfToken: 'tok' }),
    conversationCall: async (target, method) => {
      if (method === 'ListConversations') return { conversations: [] }
      if (method === 'CreateConversation') {
        if (target.httpPort === 1) throw Object.assign(new Error('refused'), { code: 'ECONNREFUSED' })
        return { conversation: { id: freshId, state: { runState: 'RUN_STATE_IDLE' } } }
      }
      return {}
    },
    call: async () => ({ trajectorySummaries: {} }),
  })
  t.after(() => runtime.stop())
  await runtime.start()
  await node.ready
  const started = await node.peer.call('session.start', { model: 'pro' })
  assert.equal(started.session.session_id, 'desktop-agy-' + freshId)
  assert.equal(discoverCalls, 2)
})

test('extractModelEntries and resolveModel handle aliases, effort levels, and invalid options', () => {
  const { entries, defaultId } = extractModelEntries({
    clientModelConfigs: [
      { label: 'Skip Enum', modelOrAlias: { model: 'MODEL_PLACEHOLDER_M1' } },
      { label: 'Gemini 4 Neon (High)', modelOrAlias: { alias: 'gemini-4-neon-high' } },
      { label: 'Gemini 4 Neon (Low)', modelOrAlias: { alias: 'gemini-4-neon-low' } },
    ],
    defaultOverrideModelConfig: { modelOrAlias: { alias: 'gemini-4-neon-low' } },
  })
  assert.deepEqual(entries, [
    { id: 'gemini-4-neon-high', name: 'Gemini 4 Neon (High)' },
    { id: 'gemini-4-neon-low', name: 'Gemini 4 Neon (Low)' },
  ])
  assert.equal(defaultId, 'gemini-4-neon-low')

  const catalog = [
    { id: 'gemini-4-neon-high', name: 'Gemini 4 Neon (High)', default: true },
    { id: 'gemini-4-neon-low', name: 'Gemini 4 Neon (Low)', default: false },
    { id: 'pro', name: 'Antigravity Pro (Auto)', default: false },
  ]
  assert.equal(resolveModel('gemini-4-neon-high', 'low', catalog), 'gemini-4-neon-low')
  assert.equal(resolveModel('gemini-4-neon', 'low', catalog), 'gemini-4-neon-low')
  assert.equal(resolveModel('pro', 'high', catalog), 'pro')
  assert.equal(resolveModel('', '', catalog), 'gemini-4-neon-high')
  assert.equal(resolveModel('custom-model', '', catalog), 'custom-model')

  assert.throws(() => createDesktopRuntime({ runtime: 'bad id' }), /Unsupported Antigravity desktop runtime id/)
  assert.throws(() => createDesktopRuntime({ launcher: '1bad' }), /Unsupported Antigravity desktop launcher name/)
})

test('desktop handles offline hub, paginated listing, unsettled interrupt, and invalid CreateConversation responses', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'desktop-agy-edge-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const page1Id = '88888888-9999-4aaa-8bbb-cccccccccccc'
  const page2Id = '99999999-aaaa-4bbb-8ccc-dddddddddddd'
  let online = false
  const updates = []
  const node = await startFakeNode(t)

  const runtime = createDesktopRuntime({
    dir,
    socketPath: node.path,
    workingPollMs: 0,
    interruptPollMs: 5,
    interruptTimeoutMs: 25,
    discover: async () => (online ? { httpPort: 12345, csrfToken: 'tok' } : undefined),
    conversationCall: async (_, method, params) => {
      if (method === 'ListConversations') {
        if (!params.pageToken) {
          return {
            conversations: [{
              id: page1Id,
              metadata: { createTime: new Date().toISOString() },
              annotations: { title: 'Page 1' },
              state: { runState: 'RUN_STATE_RUNNING', updateTime: new Date().toISOString() },
            }],
            nextPageToken: 'p2',
          }
        }
        return {
          conversations: [{
            id: page2Id,
            metadata: { createTime: new Date().toISOString() },
            annotations: { title: 'Page 2' },
            state: { runState: 'RUN_STATE_IDLE', updateTime: new Date().toISOString() },
          }],
        }
      }
      if (method === 'CreateConversation') return { conversation: { id: 'not-a-uuid' } }
      if (method === 'GetConversation') {
        if (params.conversationId === page1Id) return { conversation: { id: page1Id, state: { runState: 'RUN_STATE_RUNNING' } } }
        throw new Error('missing')
      }
      if (method === 'UpdateConversation') {
        updates.push(params)
        return {}
      }
      return {}
    },
    call: async () => ({ trajectorySummaries: {} }),
  })
  t.after(() => runtime.stop())
  await runtime.start()
  await node.ready

  await waitUntil('offline yields empty list', () => Array.isArray(node.last('runtime.sessions')?.sessions) && node.last('runtime.sessions').sessions.length === 0)
  await assert.rejects(node.peer.call('session.start', { prompt: 'hi' }), /Antigravity Desktop is offline/)
  await assert.rejects(node.peer.call('launchers.models', { launcher: 'antigravity-desktop', refresh: true }), /Antigravity Desktop is offline/)

  online = true
  await runtime.pushNow()
  await waitUntil('both pages listed', () => node.last('runtime.sessions')?.sessions?.length === 2)
  assert.deepEqual(
    node.last('runtime.sessions').sessions.map(s => s.session_id).sort(),
    ['desktop-agy-' + page1Id, 'desktop-agy-' + page2Id].sort(),
  )

  await assert.rejects(node.peer.call('session.start', { model: 'pro' }), /returned no conversation ID/)
  await assert.rejects(
    node.peer.call('session.send', { session_id: 'desktop-agy-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee', text: 'hi' }),
    { code: 'not_found' },
  )

  // Blank rename is a clean no-op.
  await node.peer.call('session.rename', { session_id: 'desktop-agy-' + page1Id, title: '   ' })
  assert.equal(updates.length, 0)

  // Interrupt that stays running past deadline resolves without settled: true.
  const interrupted = await node.peer.call('session.interrupt', { session_id: 'desktop-agy-' + page1Id })
  assert.deepEqual(interrupted, { ok: true })
})

test('desktop.mjs CLI forwards runtime, launcher, display-name, and model flags and exits cleanly on SIGTERM', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'desktop-agy-cli-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const node = await startFakeNode(t)
  const child = spawn(process.execPath, [
    DESKTOP_ENTRY,
    '--socket', node.path,
    '--desktop-dir', dir,
    '--runtime', 'antigravity-desktop-alt',
    '--launcher', 'antigravity-desktop-alt',
    '--display-name', 'Desktop Alt',
    '--model', 'pro',
  ], { stdio: ['ignore', 'pipe', 'pipe'] })
  t.after(async () => {
    if (child.exitCode === null) {
      child.kill('SIGTERM')
      await new Promise(resolve => child.once('exit', resolve))
    }
  })

  await node.ready
  assert.equal(node.hello.runtime, 'antigravity-desktop-alt')
  assert.equal(node.hello.launcher, 'antigravity-desktop-alt')
  assert.equal(node.hello.launcher_model, 'pro')
  assert.equal(node.hello.name, 'Desktop Alt')
  assert.equal(node.hello.caps.start, true)
  assert.equal(node.hello.caps.models, true)
  assert.equal(node.hello.caps.archive, true)

  child.kill('SIGTERM')
  const [code] = await new Promise(resolve => child.once('exit', (...args) => resolve(args)))
  assert.equal(code, 0)
})

test('desktop merges trajectory-only sessions when ListConversations returns empty or omits them, while keeping archived/probe rows excluded', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'desktop-agy-merge-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const legacyId = 'aaaaaaaa-1111-4222-8333-444444444444'
  const probeId = 'bbbbbbbb-1111-4222-8333-444444444444'
  const archivedId = 'cccccccc-1111-4222-8333-444444444444'
  const legacySession = 'desktop-agy-' + legacyId
  const nowIso = new Date().toISOString()
  const cascadeConfig = { plannerConfig: { planModel: 'gemini-4-neon-high' } }
  const sent = []
  let conversations = []
  const node = await startFakeNode(t)

  const runtime = createDesktopRuntime({
    dir,
    socketPath: node.path,
    workingPollMs: 0,
    discover: async () => ({ httpPort: 12345, csrfToken: 'tok' }),
    conversationCall: async (_, method) => {
      if (method === 'ListConversations') return { conversations }
      return {}
    },
    call: async (_, method, params) => {
      if (method === 'GetAllCascadeTrajectories') {
        return {
          trajectorySummaries: {
            [legacyId]: {
              summary: 'Desktop UI conversation',
              lastModifiedTime: nowIso,
              stepCount: 2,
              workspaces: [{ workspaceFolderAbsoluteUri: 'file:///workspace/desktop-repo' }],
            },
            [probeId]: { summary: 'Probe trajectory', lastModifiedTime: nowIso, stepCount: 1 },
            [archivedId]: { summary: 'Archived trajectory', lastModifiedTime: nowIso, stepCount: 1 },
          },
        }
      }
      if (method === 'GetCascadeTrajectorySteps') {
        return { steps: [{ userInput: { userConfig: cascadeConfig } }] }
      }
      if (method === 'SendUserCascadeMessage') {
        sent.push(params)
        return {}
      }
      return {}
    },
  })
  t.after(() => runtime.stop())
  await runtime.start()
  await node.ready

  // Case 1: ListConversations returns [] while trajectories are non-empty.
  await waitUntil('trajectory sessions merged when ListConversations is empty', () =>
    node.last('runtime.sessions')?.sessions?.some(s => s.session_id === legacySession))
  const row = node.last('runtime.sessions').sessions.find(s => s.session_id === legacySession)
  assert.equal(row.title, 'Desktop UI conversation')
  assert.equal(row.cwd, '/workspace/desktop-repo')

  // Existing Desktop-UI session remains attachable and sendable with its cascadeConfig.
  await node.peer.call('session.send', { session_id: legacySession, text: 'hello legacy', client_msg_id: 'legacy-1' })
  assert.deepEqual(sent, [
    { cascadeId: legacyId, items: [{ text: 'hello legacy' }], cascadeConfig, clientMessageId: 'legacy-1' },
  ])

  // Case 2: Probe and archived rows returned by ListConversations are excluded even if still in trajectories.
  conversations = [
    { id: probeId, metadata: { tags: ['persona-probe'], createTime: nowIso }, state: { runState: 'RUN_STATE_IDLE', updateTime: nowIso } },
    { id: archivedId, annotations: { archived: true }, metadata: { createTime: nowIso }, state: { runState: 'RUN_STATE_IDLE', updateTime: nowIso } },
  ]
  await runtime.pushNow()
  await waitUntil('probe and archived rows filtered while legacy trajectory remains', () => {
    const ids = node.last('runtime.sessions')?.sessions?.map(s => s.session_id) || []
    return ids.length === 1 && ids[0] === legacySession
  })

  // Case 3: Archiving legacySession keeps it hidden even though it stays in trajectorySummaries; unarchiving restores it.
  await node.peer.call('session.archive', { session_id: legacySession })
  await waitUntil('archived trajectory session hidden', () => node.last('runtime.sessions')?.sessions?.length === 0)
  await node.peer.call('session.unarchive', { session_id: legacySession })
  await waitUntil('unarchived trajectory session restored', () =>
    node.last('runtime.sessions')?.sessions?.length === 1 &&
    node.last('runtime.sessions').sessions[0].session_id === legacySession)
})

test('desktop findHub skips symlinked and null-JSON hub files, postJson rejects non-object/oversized replies, and send wraps context cleanly', async t => {
  // 1. postJson rejects null/primitive JSON and oversized (>16 MiB) responses cleanly once.
  const rpcServer = createServer((req, res) => {
    if (req.url?.endsWith('/NullResp')) {
      res.writeHead(200, { 'content-type': 'application/json' }).end('null')
      return
    }
    if (req.url?.endsWith('/PrimitiveResp')) {
      res.writeHead(200, { 'content-type': 'application/json' }).end('42')
      return
    }
    if (req.url?.endsWith('/ArrayResp')) {
      res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify([{ id: 'model-a', name: 'Model A' }]))
      return
    }
    if (req.url?.endsWith('/TruncatedResp')) {
      res.writeHead(200, { 'content-type': 'application/json', 'content-length': '100' })
      res.write('{"partial":')
      setTimeout(() => req.socket.destroy(), 5)
      return
    }
    if (req.url?.endsWith('/HugeResp')) {
      res.writeHead(200, { 'content-type': 'application/json' })
      const chunk = Buffer.alloc(1024 * 1024, 'a')
      for (let i = 0; i < 18; i++) {
        if (res.destroyed) break
        res.write(chunk)
      }
      res.end()
      return
    }
    if (req.url?.endsWith('/ListConversations')) {
      res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify({ conversations: [] }))
      return
    }
    if (req.url?.endsWith('/GetAllCascadeTrajectories')) {
      res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify({ trajectorySummaries: {} }))
      return
    }
    res.writeHead(200, { 'content-type': 'application/json' }).end('{}')
  })
  await new Promise(resolve => rpcServer.listen(0, '127.0.0.1', resolve))
  t.after(() => new Promise(resolve => rpcServer.close(resolve)))
  const port = rpcServer.address().port
  const hub = { httpPort: port, csrfToken: 'tok-valid' }

  await assert.rejects(rpc(hub, 'NullResp', {}), /returned non-object JSON/)
  await assert.rejects(rpc(hub, 'PrimitiveResp', {}), /returned non-object JSON/)
  await assert.rejects(rpc(hub, 'HugeResp', {}), /Desktop RPC response is too large/)
  await assert.rejects(rpc(hub, 'TruncatedResp', {}), /response truncated|aborted|socket hang up|ECONNRESET/i)
  assert.deepEqual(await conversationRpc(hub, 'ArrayResp', {}), [{ id: 'model-a', name: 'Model A' }])

  // 2. Default discover (findHub) skips a symlink hub file (ELOOP), a JSON `null` hub file, and a hub directory (EISDIR), and finds the live hub.
  const dir = mkdtempSync(join(tmpdir(), 'desktop-agy-findhub-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const daemonDir = join(dir, 'daemon')
  mkdirSync(daemonDir, { recursive: true })
  const dummyTarget = join(dir, 'outside.json')
  writeFileSync(dummyTarget, '{}', { mode: 0o600 })
  symlinkSync(dummyTarget, join(daemonDir, 'hub_1.json'))
  writeFileSync(join(daemonDir, 'hub_2.json'), 'null', { mode: 0o600 })
  mkdirSync(join(daemonDir, 'hub_3.json'))
  const validHubPath = join(daemonDir, `hub_${port}.json`)
  writeFileSync(validHubPath, JSON.stringify({ pid: process.pid, httpPort: port, csrfToken: 'tok-valid' }), { mode: 0o600 })
  chmodSync(validHubPath, 0o600)

  const nodeFind = await startFakeNode(t)
  const rtFind = createDesktopRuntime({ dir, socketPath: nodeFind.path, workingPollMs: 0 })
  t.after(() => rtFind.stop())
  await rtFind.start()
  await nodeFind.ready
  await waitUntil('default findHub discovers live hub despite symlink and null-JSON files', () =>
    Array.isArray(nodeFind.last('runtime.sessions')?.sessions))

  // 3. send() wraps params.context with persona-context envelope (ignoring whitespace-only context) so cleanUserText & client_msg_id match.
  const key = '22222222-3333-4444-8555-666666666666'
  const sid = 'desktop-agy-' + key
  const logsDir = join(dir, 'brain', key, '.system_generated', 'logs')
  mkdirSync(logsDir, { recursive: true })
  const transcriptPath = join(logsDir, 'transcript.jsonl')
  writeFileSync(transcriptPath, '')
  const sentPayloads = []
  const nodeSend = await startFakeNode(t)
  const rtSend = createDesktopRuntime({
    dir,
    socketPath: nodeSend.path,
    workingPollMs: 0,
    discover: async () => hub,
    conversationCall: async () => ({ conversations: [] }),
    call: async (_, method, params) => {
      if (method === 'GetAllCascadeTrajectories') {
        return { trajectorySummaries: { [key]: { summary: 'ctx test', lastModifiedTime: new Date().toISOString(), stepCount: 0 } } }
      }
      if (method === 'GetCascadeTrajectorySteps') {
        return { steps: [{ userInput: { userConfig: { plannerConfig: { planModel: 'pro' } } } }] }
      }
      if (method === 'SendUserCascadeMessage') {
        sentPayloads.push(params)
        return {}
      }
      return {}
    },
  })
  t.after(() => rtSend.stop())
  await rtSend.start()
  await nodeSend.ready

  await nodeSend.peer.call('session.send', { session_id: sid, context: '   \n  ', text: 'plain msg', client_msg_id: 'cid-1' })
  assert.equal(sentPayloads[0].items[0].text, 'plain msg')
  writeFileSync(
    transcriptPath,
    JSON.stringify({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', content: 'plain msg' }) + '\n',
  )

  await nodeSend.peer.call('session.send', { session_id: sid, context: 'Branch: main', text: 'check diff', client_msg_id: 'cid-2' })
  const wrapped = sentPayloads[1].items[0].text
  assert.match(wrapped, /^Content of persona-context:\/\/\/snapshot\.txt:\n<persona-context>\nBranch: main\n<\/persona-context>\n\ncheck diff$/)
  assert.equal(cleanUserText(wrapped), 'check diff')

  // Transcript readback matches recorded client_msg_id because cleanUserText strips the wrapped context.
  appendFileSync(
    transcriptPath,
    JSON.stringify({ step_index: 1, type: 'USER_INPUT', source: 'USER_EXPLICIT', content: wrapped }) + '\n',
  )
  const pageRes = await nodeSend.peer.call('session.items', { session_id: sid })
  assert.equal(pageRes.items[0].client_msg_id, 'cid-1')
  assert.equal(pageRes.items[1].text, 'check diff')
  assert.equal(pageRes.items[1].client_msg_id, 'cid-2')

  // Two <persona-context> blocks with middle text and lowercase context headers must still be wrapped in an outer envelope
  // so cleanUserText strips all context cleanly without leaking middle text into the user prompt.
  const twoBlockCtx = '<persona-context>a</persona-context>\nmiddle\n<persona-context>b</persona-context>'
  await nodeSend.peer.call('session.send', { session_id: sid, context: twoBlockCtx, text: 'after two blocks', client_msg_id: 'cid-3' })
  const wrappedTwo = sentPayloads[2].items[0].text
  assert.equal(cleanUserText(wrappedTwo), 'after two blocks')

  const lowerEnvCtx = 'content of persona-context:///snapshot.txt:\n<persona-context>x</persona-context>'
  await nodeSend.peer.call('session.send', { session_id: sid, context: lowerEnvCtx, text: 'after lower env', client_msg_id: 'cid-4' })
  const wrappedLower = sentPayloads[3].items[0].text
  assert.equal(cleanUserText(wrappedLower), 'after lower env')
})
