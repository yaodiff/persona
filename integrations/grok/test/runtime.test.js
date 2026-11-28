import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { test } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createGrokRuntime, isConversationDir } from '../lib/runtime.js'
import { parseTranscript } from '../lib/map.js'

const RAW = '11111111-2222-4333-8444-555555555555'
const ID = `grok-chat-${RAW}`
const CATALOG = [{ modelId: 'fast-model', name: 'Fast model', _meta: { reasoningEffort: 'high', reasoningEfforts: [{ id: 'low' }, { id: 'high' }, { id: 'xhigh' }] } }]
class FakeAcp extends EventEmitter {
  generation = 1
  calls = []
  prompts = []
  state = { model: 'fast-model', reasoning_effort: 'high' }
  async ready() { return { _meta: { modelState: { currentModelId: this.state.model, availableModels: CATALOG } } } }
  config() { return { models: { currentModelId: this.state.model, availableModels: CATALOG }, configOptions: Object.entries(this.state).map(([id, currentValue]) => ({ id, currentValue })) } }
  async call(method, params, timeout) {
    this.calls.push({ method, params })
    if (method === 'session/new') return { sessionId: RAW, ...this.config() }
    if (method === 'session/load') return this.config()
    if (method === 'session/set_config_option') { assert.equal(typeof params.value, 'string'); this.state[params.configId] = params.value; return this.config() }
    if (method === '_x.ai/session/rename') {
      const file = join(this.grokHome, 'sessions', encodeURIComponent(params.cwd), params.sessionId, 'summary.json')
      const summary = JSON.parse(await readFile(file, 'utf8'))
      // Grok preserves an existing summary on later renames; generated_title is the current manual name.
      await writeFile(file, JSON.stringify({ ...summary, generated_title: params.title, session_summary: summary.session_summary || params.title,
        title_is_manual: true, updated_at: new Date().toISOString() }))
      return { success: true }
    }
    if (method === 'session/prompt') return new Promise((resolve, reject) => {
      const timer = timeout ? setTimeout(() => reject(new Error('Grok session/prompt timed out')), timeout) : null
      this.prompts.push({ params, resolve: (value) => { clearTimeout(timer); resolve(value) }, reject: (error) => { clearTimeout(timer); reject(error) } })
    })
    return {}
  }
  notify(method) { if (method === 'session/cancel') this.prompts.at(-1)?.resolve({ stopReason: 'cancelled' }) }
  update(update) { this.emit('notification', 'session/update', { sessionId: RAW, update }) }
  async stop() { for (const p of this.prompts) p.resolve({ stopReason: 'cancelled' }) }
}

async function fixture(t, opts = {}) {
  const home = await mkdtemp(join(tmpdir(), 'persona-grok-test-'))
  const cwd = join(home, '.persona', 'assistant')
  await mkdir(cwd, { recursive: true })
  const node = await startFakeNode(t, { strict: true, tools: [{ name: 'test_tool', description: 'A harmless test', input_schema: { type: 'object' } }] })
  const acp = new FakeAcp()
  acp.grokHome = join(home, '.grok')
  const runtime = createGrokRuntime({ home, env: {}, grokHome: join(home, '.grok'), socketPath: node.path, acp, model: 'fast-model', effort: 'xhigh', refreshMs: 60_000, ...opts })
  runtime.start()
  await node.ready
  t.after(async () => { await runtime.stop(); await rm(home, { recursive: true, force: true }) })
  const start = () => node.peer.call('session.start', { cwd, assistant: true, permission_mode: 'bypassPermissions' })
  const send = (text, extra = {}) => node.peer.call('session.send', { session_id: ID, text, ...extra })
  const done = async (text = 'Reply') => {
    acp.update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } })
    acp.prompts.at(-1).resolve({ stopReason: 'end_turn' })
    await until('turn settled', () => !runtime.sessions.get(ID).running)
  }
  return { home, cwd, node, acp, runtime, start, send, done }
}

async function nativeSummary(f) {
  const dir = join(f.home, '.grok', 'sessions', encodeURIComponent(f.cwd), RAW)
  const file = join(dir, 'summary.json')
  await mkdir(dir, { recursive: true })
  await writeFile(file, JSON.stringify({ info: { id: RAW, cwd: f.cwd }, generated_title: 'Original title', session_summary: 'An earlier conversation summary',
    current_model_id: 'fast-model', reasoning_effort: 'xhigh', updated_at: new Date().toISOString() }))
  return { dir, file }
}

test('the native process uses the selected Grok home while retaining its login environment', async (t) => {
  const home = await mkdtemp(join(tmpdir(), 'persona-grok-env-'))
  const grokHome = join(home, 'custom-grok')
  const output = join(home, 'child-env.json')
  const script = `
    const { createInterface } = require('node:readline');
    require('node:fs').writeFileSync(process.argv[1], JSON.stringify({
      grokHome: process.env.GROK_HOME, path: process.env.PATH, home: process.env.HOME,
    }), { mode: 0o600 });
    createInterface({ input: process.stdin }).on('line', (line) => {
      const msg = JSON.parse(line);
      if (msg.method === 'initialize') process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: msg.id,
        result: { agentCapabilities: { _meta: { 'x.ai/hooks': { blockingEvents: ['pre_tool_use'] } } } },
      }) + '\\n');
    });
  `
  const runtime = createGrokRuntime({ home, env: {}, grokHome, bin: process.execPath, args: ['-e', script, output] })
  t.after(async () => { await runtime.stop(); await rm(home, { recursive: true, force: true }) })
  await runtime.driver.models()
  const observed = JSON.parse(await readFile(output, 'utf8'))
  assert.equal(observed.grokHome, grokHome)
  assert.equal(observed.path, process.env.PATH)
  assert.equal(observed.home, process.env.HOME)
})

test('session rename advertises support, publishes the native title, and survives scans and rediscovery', async (t) => {
  const f = await fixture(t)
  assert.equal(f.node.hello.caps.rename, true)
  await f.start()
  await f.node.peer.call('session.modes', { session_id: ID, permission_mode: 'plan' })
  const { dir, file } = await nativeSummary(f)
  const transcript = [
    { sessionUpdate: 'user_message_chunk', content: { type: 'text', text: 'Keep this question' } },
    { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Keep this answer' } },
  ].map((update) => JSON.stringify({ method: 'session/update', params: { update } })).join('\n') + '\n'
  await writeFile(join(dir, 'updates.jsonl'), transcript)
  const metadata = await readFile(join(dir, 'persona-runtime.json'), 'utf8')
  assert.equal((await f.runtime.driver.listSessions())[0].title, 'Original title')
  const priorCalls = f.acp.calls.length
  assert.deepEqual(await f.node.peer.call('session.rename', { session_id: ID, title: '  修复后的标题  ' }), { ok: true })
  assert.deepEqual(f.acp.calls.slice(priorCalls), [{ method: '_x.ai/session/rename', params: { sessionId: RAW, title: '修复后的标题', cwd: f.cwd } }])
  await until('renamed title pushed', () => f.node.last('runtime.sessions')?.sessions?.find((s) => s.session_id === ID)?.title === '修复后的标题')
  const summary = JSON.parse(await readFile(file, 'utf8'))
  assert.equal(summary.generated_title, '修复后的标题')
  assert.equal(summary.session_summary, 'An earlier conversation summary')
  assert.equal(summary.title_is_manual, true)
  assert.equal((await f.runtime.driver.listSessions())[0].title, '修复后的标题')
  await f.node.peer.call('session.rename', { session_id: ID, title: '最终标题' })
  const restarted = createGrokRuntime({ home: f.home, env: {}, grokHome: f.acp.grokHome, acp: new FakeAcp() })
  t.after(() => restarted.stop())
  const restored = (await restarted.driver.listSessions())[0]
  assert.equal(restored.session_id, ID)
  assert.equal(restored.title, '最终标题')
  assert.equal(restored.permission_mode, 'plan')
  assert.equal(restored.model, 'fast-model')
  assert.equal(restored.effort, 'xhigh')
  assert.equal(restarted.sessions.get(ID).assistant, true)
  assert.deepEqual((await restarted.driver.items({ session_id: ID })).items.map((i) => i.text), ['Keep this question', 'Keep this answer'])
  assert.equal(await readFile(join(dir, 'updates.jsonl'), 'utf8'), transcript)
  assert.equal(await readFile(join(dir, 'persona-runtime.json'), 'utf8'), metadata)
})

test('renaming during an active prompt does not interrupt or restart the conversation', async (t) => {
  const f = await fixture(t)
  await f.start()
  await nativeSummary(f)
  await f.send('Keep working', { client_msg_id: 'ongoing' })
  await until('prompt starts', () => f.acp.prompts.length === 1)
  const pending = f.acp.prompts[0]
  const notifications = []
  const notify = f.acp.notify.bind(f.acp)
  f.acp.notify = (...args) => { notifications.push(args); return notify(...args) }
  const priorCalls = f.acp.calls.length
  await f.node.peer.call('session.rename', { session_id: ID, title: 'Work in progress' })
  assert.deepEqual(f.acp.calls.slice(priorCalls).map((c) => c.method), ['_x.ai/session/rename'])
  assert.deepEqual(notifications, [])
  assert.equal(f.runtime.sessions.get(ID).running, true)
  assert.equal(f.acp.prompts.length, 1)
  assert.equal(f.acp.prompts[0], pending)
  await until('working renamed title pushed', () => f.node.last('runtime.sessions')?.sessions?.some((s) => s.session_id === ID && s.title === 'Work in progress' && s.status === 'working'))
  await f.done('Still completed normally')
  await f.runtime.sessions.get(ID).tail
  assert.equal((await f.runtime.driver.listSessions())[0].title, 'Work in progress')
  assert.deepEqual(f.runtime.sessions.get(ID).items.map((i) => i.text), ['Keep working', 'Still completed normally'])
})

test('rename rejects invalid titles and unavailable conversations before calling Grok', async (t) => {
  const f = await fixture(t)
  await f.start()
  const priorCalls = f.acp.calls.length
  for (const title of ['', ' \n\t ', null, 42, { title: 'Wrong type' }]) {
    await assert.rejects(f.node.peer.call('session.rename', { session_id: ID, title }), (error) => error.code === 'invalid_params')
  }
  await assert.rejects(f.node.peer.call('session.rename', { session_id: 'grok-chat-00000000-0000-4000-8000-000000000000', title: 'Missing' }), (error) => error.code === 'not_found')
  assert.equal(f.acp.calls.length, priorCalls)
})

test('failed or unacknowledged native rename keeps the old title and native record', async (t) => {
  const f = await fixture(t)
  await f.start()
  const { file } = await nativeSummary(f)
  assert.equal((await f.runtime.driver.listSessions())[0].title, 'Original title')
  const original = await readFile(file, 'utf8')
  const call = f.acp.call.bind(f.acp)
  for (const result of [new Error('Rename unavailable'), {}, { success: false }]) {
    f.acp.call = async (method, ...args) => {
      if (method !== '_x.ai/session/rename') return call(method, ...args)
      if (result instanceof Error) throw result
      return result
    }
    await assert.rejects(f.node.peer.call('session.rename', { session_id: ID, title: 'Do not publish' }), /Rename unavailable|did not rename/)
    assert.equal(f.runtime.sessions.get(ID).title, 'Original title')
    assert.equal((await f.runtime.driver.listSessions())[0].title, 'Original title')
    assert.equal(await readFile(file, 'utf8'), original)
  }
})

test('the assistant launcher is named for people and says only the Agent launcher it was set up for', async (t) => {
  const plain = await fixture(t)
  assert.deepEqual(plain.node.hello.launchers, [{ name: 'grok-chat', agent: 'grok', display_name: 'Grok', model: 'fast-model' }])
  const f = await fixture(t, { displayName: 'Grok assistant', assistantFor: 'grok' })
  assert.deepEqual(f.node.hello.launchers, [{ name: 'grok-chat', agent: 'grok', display_name: 'Grok assistant', assistant_for: 'grok', model: 'fast-model' }])
  await f.start()
  const changed = await until('launchers with models', () => f.node.all('runtime.launchers').at(-1)?.launchers?.[0]?.models)
  assert.equal(f.node.last('runtime.launchers').launchers[0].assistant_for, 'grok')
  assert.equal(f.node.last('runtime.launchers').launchers[0].display_name, 'Grok assistant')
  assert.ok(changed.some((m) => m.id === 'fast-model'))
  for (const assistantFor of ['bad name!', 'grok-chat']) assert.throws(() => createGrokRuntime({ acp: new FakeAcp(), assistantFor }), /assistant-for/)
})

test('assistant startup selects exact model/effort and forwards context, images and scoped tools', async (t) => {
  const f = await fixture(t)
  assert.equal(f.node.hello.caps.assistant, true)
  assert.equal(f.node.hello.launcher, 'grok-chat')
  const { session } = await f.start()
  assert.equal(session.model, 'fast-model')
  assert.equal(session.effort, 'xhigh')
  assert.deepEqual(f.acp.calls.find((c) => c.method === 'session/set_config_option').params, { sessionId: RAW, configId: 'reasoning_effort', value: 'xhigh' })
  const servers = f.acp.calls.find((c) => c.method === 'session/load').params.mcpServers
  assert.equal(servers.length, 1)
  assert.equal(servers[0].name, 'persona')
  const response = await fetch(servers[0].url, { method: 'POST', headers: Object.fromEntries(servers[0].headers.map((h) => [h.name, h.value])),
    body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'test_tool', arguments: { a: 1 } } }) })
  assert.equal((await response.json()).result.content[0].text, 'test_tool ran')
  assert.deepEqual(f.node.toolCalls[0], { name: 'test_tool', args: { a: 1 }, session_id: ID, assistant: true })
  const unauthorized = await fetch(servers[0].url, { method: 'POST', body: '{}' })
  assert.equal(unauthorized.status, 401)
  await f.send('Hello', { client_msg_id: 'msg-1', context: '<persona-context>private snapshot</persona-context>', images: [{ data: 'aGVsbG8=', media_type: 'image/png' }] })
  await until('prompt sent', () => f.acp.prompts.length)
  assert.equal(f.acp.prompts[0].params.prompt[0].text, '<persona-context>private snapshot</persona-context>\n\nHello')
  assert.deepEqual(f.acp.prompts[0].params.prompt[1], { type: 'image', data: 'aGVsbG8=', mimeType: 'image/png' })
  assert.equal(f.runtime.sessions.get(ID).items[0].text, 'Hello')
  assert.equal(f.runtime.sessions.get(ID).items[0].client_msg_id, 'msg-1')
  const imagePage = await f.node.peer.call('session.items', { session_id: ID })
  const picture = imagePage.items.find((i) => i.kind === 'image')
  assert.equal(picture.image.lazy, true)
  assert.equal(picture.image.data, '')
  const image = await f.node.peer.call('session.image', { session_id: ID, item_id: picture.id })
  assert.deepEqual(image, { mime: 'image/png', data: 'aGVsbG8=' })
  await f.done()
})

test('tool boundaries reset streaming text; continuations keep the same session and deduplicate sends', async (t) => {
  const f = await fixture(t)
  await f.start()
  await f.node.peer.call('session.watch', { session_id: ID })
  await f.send('First', { client_msg_id: 'one' })
  await f.send('First', { client_msg_id: 'one' })
  await until('first prompt', () => f.acp.prompts.length === 1)
  f.acp.update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Preparing' } })
  await until('first stream', () => f.node.last('session.delta')?.text === 'Preparing')
  f.acp.update({ sessionUpdate: 'tool_call', toolCallId: 't1', title: 'read_file', rawInput: { path: '/example' } })
  f.acp.update({ sessionUpdate: 'tool_call_update', toolCallId: 't1', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'file' } }] })
  await f.done('Complete')
  const texts = f.runtime.sessions.get(ID).items.filter((i) => i.role === 'assistant' && i.kind === 'text').map((i) => i.text)
  assert.deepEqual(texts, ['Preparing', 'Complete'])
  await f.send('Second', { client_msg_id: 'two' })
  await until('second prompt', () => f.acp.prompts.length === 2)
  assert.equal(f.acp.prompts[1].params.sessionId, RAW)
  await f.done('Second reply')
  assert.equal(f.acp.calls.filter((c) => c.method === 'session/new').length, 1)
})

test('interrupt cancels the active turn, drops queued turns, and allows a later retry', async (t) => {
  const f = await fixture(t)
  await f.start()
  await f.send('First')
  await until('first prompt', () => f.acp.prompts.length === 1)
  assert.equal((await f.send('Second', { client_msg_id: 'queued' })).queued, true)
  assert.equal((await f.node.peer.call('session.interrupt', { session_id: ID })).settled, true)
  assert.equal(f.acp.prompts.length, 1)
  assert.equal(f.runtime.sessions.get(ID).queued, 0)
  // The SDK's transport dedup preserves accepted delivery; a user retry carries a fresh id.
  await f.send('Second', { client_msg_id: 'retry' })
  await until('retry starts', () => f.acp.prompts.length === 2)
  await f.done()
})

test('tool hooks enforce read-only and gate shell commands even in bypass mode', async (t) => {
  const f = await fixture(t)
  f.node.tools.push({ name: 'mail_send', shell: ['mail-gate', '/skills/mail.sh'] })
  await f.start()
  await f.send('Read only', { mode: 'plan' })
  await until('prompt starts', () => f.acp.prompts.length)
  const hook = (toolName, toolInput) => f.acp.request('_x.ai/hooks/run', { sessionId: RAW, toolName, toolInput, toolUseId: toolName })
  assert.equal((await hook('write_file', { path: '/tmp/write' })).decision, 'deny')
  assert.deepEqual(await hook('read_file', { path: '/tmp/read' }), {})
  await f.node.peer.call('session.modes', { session_id: ID, permission_mode: 'bypassPermissions' })
  assert.equal((await hook('run_terminal_command', { command: 'mail-gate send' })).decision, 'deny')
  assert.deepEqual(await hook('run_terminal_command', { command: 'printf allowed' }), {})
  await f.done()
})

test('default mode asks on the phone and interrupt withdraws a pending permission', async (t) => {
  const f = await fixture(t)
  await f.start()
  await f.send('Ask', { mode: 'default' })
  await until('prompt starts', () => f.acp.prompts.length)
  f.node.resolve = () => new Promise(() => {})
  const pending = f.acp.request('_x.ai/hooks/run', { sessionId: RAW, toolName: 'run_terminal_command', toolInput: { command: 'echo test' }, toolUseId: 'tool1' })
  await until('permission shown', () => f.node.asked.length === 1)
  await f.node.peer.call('session.interrupt', { session_id: ID })
  assert.equal((await pending).decision, 'deny')
  assert.ok(f.node.all('interaction.cancel').length > 0)
})

test('native transcript reload keeps failures and discovers a continued assistant chat', async (t) => {
  const f = await fixture(t)
  await f.start()
  await f.send('Fail this request')
  await until('prompt starts', () => f.acp.prompts.length)
  const dir = join(f.home, '.grok', 'sessions', encodeURIComponent(f.cwd), RAW)
  await mkdir(dir, { recursive: true })
  const data = JSON.stringify({ method: 'session/update', params: { update: { sessionUpdate: 'user_message_chunk', content: { type: 'text', text: '<persona-context>private</persona-context>\n\nFail this request' } } } }) + '\n'
  await writeFile(join(dir, 'updates.jsonl'), data)
  await writeFile(join(dir, 'summary.json'), JSON.stringify({ info: { id: RAW, cwd: f.cwd }, current_model_id: 'fast-model', reasoning_effort: 'xhigh', updated_at: new Date().toISOString() }))
  f.acp.prompts[0].reject(new Error('Provider unavailable'))
  await until('failure retained', () => f.runtime.sessions.get(ID).items.some((i) => i.text === 'API Error: Provider unavailable'))
  const read = await f.node.peer.call('session.items', { session_id: ID })
  assert.equal(read.items.at(-1).text, 'API Error: Provider unavailable')
  assert.equal(read.items[0].text, 'Fail this request')
  f.runtime.sessions.clear()
  const list = await f.runtime.driver.listSessions()
  assert.equal(list[0].session_id, ID)
  assert.equal(list[0].model, 'fast-model')
  assert.equal(list[0].turn_error, 'API Error: Provider unavailable')
  assert.equal((await f.node.peer.call('session.items', { session_id: ID })).items.at(-1).text, 'API Error: Provider unavailable')
  const metaPath = join(dir, 'persona-runtime.json')
  assert.equal((await stat(metaPath)).mode & 0o777, 0o600)
  const metaText = await readFile(metaPath, 'utf8')
  assert.ok(!metaText.includes('Fail this request'), 'metadata must not store the prompt')
  await f.send('Continue')
  await until('continuation prompt', () => f.acp.prompts.length === 2)
  assert.equal(f.acp.calls.filter((c) => c.method === 'session/new').length, 1)
  await f.done()
})

test('botmux sentinel chunks keep the previous reply preview and the original transcript', () => {
  const marker = 'BOTMUX_NOTHING_TO_SEND'
  const text = (value) => ({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: value } })
  const transcript = (updates) => parseTranscript(updates.map((update) =>
    JSON.stringify({ method: 'session/update', params: { update } })).join('\n'))
  const updates = [text('Notes\n' + marker + '\nready'), { sessionUpdate: 'tool_call', toolCallId: 'read', title: 'read_file' }]
  assert.equal(transcript(updates).lastText, 'Notes ready')
  const parsed = transcript([...updates, text('BOTMUX_NOTHING_'), text('TO_SEND')])
  assert.equal(parsed.lastText, 'Notes ready')
  assert.equal(parsed.items.at(-1).text, marker)
  assert.equal(transcript([text('BOTMUX_'), text('NOTHING_TO_SEND')]).lastText, '')
})

test('native transcript separates intermediate text, tools and final answer', () => {
  const updates = [
    { sessionUpdate: 'user_message_chunk', content: { type: 'text', text: '<persona-context>hidden</persona-context>Question' } },
    { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Looking' } },
    { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: ' now' } },
    { sessionUpdate: 'tool_call', toolCallId: 't', title: 'read_file' },
    { sessionUpdate: 'tool_call_update', toolCallId: 't', status: 'completed', content: [{ type: 'content', content: { type: 'text', text: 'Read' } }] },
    { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Answer' } },
  ]
  const parsed = parseTranscript(updates.map((update) => JSON.stringify({ method: 'session/update', params: { update } })).join('\n'))
  assert.deepEqual(parsed.items.map((i) => i.text), ['Question', 'Looking now', undefined, 'Read', 'Answer'])
  assert.equal(parsed.lastText, 'Answer')
})

test('terminal tool_call_update retains output streamed in earlier updates', () => {
  const updates = [
    { sessionUpdate: 'tool_call', toolCallId: 't1', title: 'read_file' },
    { sessionUpdate: 'tool_call_update', toolCallId: 't1', status: 'in_progress', content: [{ type: 'content', content: { type: 'text', text: 'Streamed text' } }] },
    { sessionUpdate: 'tool_call_update', toolCallId: 't1', status: 'completed' },
    { sessionUpdate: 'tool_call', toolCallId: 't2', title: 'run_terminal_command' },
    { sessionUpdate: 'tool_call_update', toolCallId: 't2', status: 'in_progress', rawOutput: 'exit 1: permission denied' },
    { sessionUpdate: 'tool_call_update', toolCallId: 't2', status: 'failed', content: [] },
  ]
  const parsed = parseTranscript(updates.map((update) => JSON.stringify({ method: 'session/update', params: { update } })).join('\n'))
  const results = parsed.items.filter((i) => i.kind === 'tool_result')
  assert.deepEqual(results.map((r) => ({ id: r.tool_use_id, text: r.text, is_error: r.is_error })), [
    { id: 't1', text: 'Streamed text', is_error: false },
    { id: 't2', text: 'exit 1: permission denied', is_error: true },
  ])
})

test('unsupported reasoning effort fails without claiming the setting applied', async (t) => {
  const f = await fixture(t)
  await f.start()
  await assert.rejects(f.node.peer.call('session.modes', { session_id: ID, effort: 'ultra' }), /does not offer/)
  assert.equal(f.runtime.sessions.get(ID).effort, 'xhigh')
})

test('runtime close stops the Agent and closes the native session', async (t) => {
  const f = await fixture(t)
  await f.start()
  await f.node.peer.call('session.close', { session_id: ID })
  assert.ok(f.acp.calls.some((c) => c.method === 'session/close' && c.params.sessionId === RAW))
  assert.deepEqual(await f.runtime.driver.listSessions(), [])
  await assert.rejects(f.send('After close'), /closed/)
})

test('bypass permissions still sends native choice questions to the phone', async (t) => {
  const f = await fixture(t)
  await f.start()
  await f.send('Choose')
  await until('prompt starts', () => f.acp.prompts.length)
  f.node.resolve = (ix) => ({ answers: { [ix.questions[0].question]: 'Second option' } })
  const answer = await f.acp.request('session/request_permission', { sessionId: RAW, toolCall: { toolCallId: 'choice', title: 'Which option?' },
    options: [{ kind: 'allow_once', optionId: 'first', name: 'First option' }, { kind: 'allow_once', optionId: 'second', name: 'Second option' }] })
  assert.equal(f.node.asked[0].kind, 'question')
  assert.deepEqual(answer, { outcome: { outcome: 'selected', optionId: 'second' } })
  await f.done()
})

test('plan mode also refuses native write approvals without a prior hook', async (t) => {
  const f = await fixture(t)
  await f.start()
  await f.send('Plan only', { mode: 'plan' })
  await until('prompt starts', () => f.acp.prompts.length)
  f.node.resolve = () => ({ allow: true })
  const answer = await f.acp.request('session/request_permission', { sessionId: RAW,
    toolCall: { toolCallId: 'write', title: 'write_file', rawInput: { path: '/tmp/no-write' } }, options: [{ kind: 'allow_once', optionId: 'yes' }] })
  assert.deepEqual(answer, { outcome: { outcome: 'cancelled' } })
  assert.equal(f.node.asked.length, 0)
  await f.done()
})

test('plan mode keeps reading another session with a2a_read and refuses sending', async (t) => {
  const f = await fixture(t)
  await f.start()
  await f.send('Plan only', { mode: 'plan' })
  await until('prompt starts', () => f.acp.prompts.length)
  const [server] = f.acp.calls.find((c) => c.method === 'session/load').params.mcpServers
  const call = async (name) => (await (await fetch(server.url, { method: 'POST', headers: Object.fromEntries(server.headers.map((h) => [h.name, h.value])),
    body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: { machine_id: 'box', session_id: 'other' } } }) })).json()).result
  assert.deepEqual(await call('a2a_read'), { content: [{ type: 'text', text: 'a2a_read ran' }], isError: false })
  assert.equal((await call('a2a_send')).isError, true)
  assert.deepEqual(f.node.toolCalls.map((c) => c.name), ['a2a_read'])
  await f.done()
})

test('plan mode survives rediscovery even when new chats default to bypass', async (t) => {
  const f = await fixture(t, { mode: 'bypassPermissions' })
  await f.start()
  await f.node.peer.call('session.modes', { session_id: ID, permission_mode: 'plan' })
  const dir = join(f.home, '.grok', 'sessions', encodeURIComponent(f.cwd), RAW)
  await writeFile(join(dir, 'summary.json'), JSON.stringify({ info: { id: RAW, cwd: f.cwd }, updated_at: new Date().toISOString() }))
  f.runtime.sessions.clear()
  const rows = await f.runtime.driver.listSessions()
  assert.equal(rows[0].permission_mode, 'plan')
})

test('assistant and health directories of each manager are rediscovered', async (t) => {
  const f = await fixture(t)
  for (const name of ['assistant', 'health', 'assistant-m-abcdef12', 'health-m-abcdef12']) {
    const cwd = join(f.home, '.persona', name)
    assert.equal(isConversationDir(cwd, f.home, {}), true)
    const dir = join(f.home, '.grok', 'sessions', encodeURIComponent(cwd), RAW)
    await mkdir(dir, { recursive: true })
    await writeFile(join(dir, 'summary.json'), JSON.stringify({ info: { id: RAW, cwd }, updated_at: new Date().toISOString() }))
    f.runtime.sessions.clear()
    assert.equal((await f.runtime.driver.listSessions()).some((s) => s.cwd === cwd), true)
    await rm(dir, { recursive: true })
  }
  for (const name of ['assistant-bin', 'assistant-m-', 'assistant-m-nothex', 'health-other']) assert.equal(isConversationDir(join(f.home, '.persona', name), f.home, {}), false)
})

test('missing setting acknowledgements cannot inherit the requested model and effort', async (t) => {
  const f = await fixture(t)
  f.acp.config = () => ({ models: { availableModels: CATALOG } })
  await assert.rejects(f.start(), /did not apply the selected model/)
  assert.equal(f.runtime.sessions.get(ID).model, '')
  assert.equal(f.runtime.sessions.get(ID).effort, '')
})

test('manual check runs an isolated acknowledged model with no tools, and removes only its native record', async (t) => {
  const f = await fixture(t)
  assert.equal(f.node.hello.caps.check, true)
  const cwd = join(f.home, 'temporary-probe')
  const dir = join(f.home, '.grok', 'sessions', encodeURIComponent(cwd), RAW)
  const sibling = join(dirname(dir), 'another-session')
  await mkdir(dir, { recursive: true })
  await mkdir(sibling)
  await writeFile(join(dir, 'updates.jsonl'), 'probe')
  const pending = f.node.peer.call('launchers.check', { launcher: 'grok-chat', cwd, prompt: 'Reply OK' })
  await until('check prompt', () => f.acp.prompts.length === 1)
  assert.deepEqual(f.acp.calls.find((c) => c.method === 'session/new').params.mcpServers, [])
  assert.deepEqual(f.acp.prompts[0].params.prompt, [{ type: 'text', text: 'Reply OK' }])
  assert.equal(f.acp.state.reasoning_effort, 'xhigh')
  assert.equal(f.runtime.sessions.size, 0)
  assert.equal((await f.acp.request('_x.ai/hooks/run', { sessionId: RAW, toolName: 'write_file' })).decision, 'deny')
  f.acp.update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'OK' } })
  f.acp.prompts[0].resolve({ stopReason: 'end_turn' })
  const result = await pending
  assert.equal(result.ok, true)
  assert.equal(result.model, 'fast-model')
  assert.equal(result.answer, 'OK')
  assert.ok(f.acp.calls.some((c) => c.method === 'session/close'))
  await assert.rejects(stat(dir), { code: 'ENOENT' })
  assert.ok((await stat(sibling)).isDirectory())
})

test('manual check times out and closes its native session without publishing a successful result', async (t) => {
  const f = await fixture(t, { checkTimeoutMs: 20 })
  const result = await f.node.peer.call('launchers.check', { cwd: join(f.home, 'probe'), prompt: 'Reply OK' })
  assert.equal(result.ok, false)
  assert.match(result.error, /timed out/)
  assert.ok(f.acp.calls.some((c) => c.method === 'session/close'))
  assert.deepEqual(await f.runtime.driver.listSessions(), [])
})

async function terminalSession(f, { cwd = join(f.home, 'code', 'project'), raw = RAW, updated = new Date().toISOString(), text = 'Hello from the terminal', sessionKind } = {}) {
  const dir = join(f.home, '.grok', 'sessions', encodeURIComponent(cwd), raw)
  await mkdir(dir, { recursive: true })
  await writeFile(join(dir, 'summary.json'), JSON.stringify({ info: { id: raw, cwd }, generated_title: 'Terminal work', current_model_id: 'fast-model', updated_at: updated, session_kind: sessionKind }))
  await writeFile(join(dir, 'updates.jsonl'), JSON.stringify({ method: 'session/update', params: { update: { sessionUpdate: 'user_message_chunk', content: { type: 'text', text } } } }) + '\n')
  return { cwd, dir }
}
const holdInTerminal = (f, raw = RAW, pid = process.pid) =>
  writeFile(join(f.home, '.grok', 'active_sessions.json'), JSON.stringify([{ session_id: raw, pid, cwd: '/', opened_at: new Date().toISOString() }]))

test('conversations started in a terminal are listed outside the assistant directories, not as the assistant', async (t) => {
  const f = await fixture(t)
  const { cwd } = await terminalSession(f)
  const rows = await f.runtime.driver.listSessions()
  assert.equal(rows.length, 1)
  assert.equal(rows[0].session_id, ID)
  assert.equal(rows[0].cwd, cwd)
  assert.equal(rows[0].space_name, 'project')
  assert.equal(rows[0].title, 'Terminal work')
  assert.equal(f.runtime.sessions.get(ID).assistant, false)
  assert.equal((await f.node.peer.call('session.items', { session_id: ID })).items[0].text, 'Hello from the terminal')
})

test('headless and subagent runs are not discovered outside assistant directories', async (t) => {
  for (const sessionKind of ['headless', 'subagent']) await t.test(sessionKind, async (t) => {
    const f = await fixture(t)
    for (const cwd of [join(f.home, 'paseo-insight-recap'), join(f.home, 'code', 'worktree')]) {
      const { dir } = await terminalSession(f, { cwd, sessionKind })
      await holdInTerminal(f)
      assert.deepEqual(await f.runtime.driver.listSessions(), [])
      assert.equal(f.runtime.sessions.size, 0)
      assert.ok((await stat(join(dir, 'summary.json'))).isFile())
      await rm(dir, { recursive: true })
    }
    assert.deepEqual(f.acp.calls, [])
  })
})

test('deleted native sessions leave the next list and notify the node without ACP close', async (t) => {
  for (const missing of ['summary', 'session', 'cwd group', 'sessions root']) await t.test(missing, async (t) => {
    const f = await fixture(t)
    // Drain the startup publication so this test cannot pass merely because that push discovers the new native session.
    await until('initial sessions published', () => f.node.last('runtime.sessions')?.sessions?.length === 0)
    const { dir } = await terminalSession(f)
    assert.equal((await f.runtime.driver.listSessions())[0].status, 'done')
    await until('terminal row published', () => f.node.last('runtime.sessions')?.sessions?.some((s) => s.session_id === ID))
    const path = missing === 'summary' ? join(dir, 'summary.json') : missing === 'session' ? dir : missing === 'cwd group' ? dirname(dir) : dirname(dirname(dir))
    await rm(path, { recursive: true })
    assert.deepEqual(await f.runtime.driver.listSessions(), [])
    assert.equal(f.runtime.sessions.has(ID), false)
    await until('removed row published', () => f.node.last('runtime.sessions')?.sessions?.length === 0)
    assert.deepEqual(f.acp.calls, [])
  })
})

test('queued and running conversations survive missing summaries until their turns settle', async (t) => {
  const f = await fixture(t)
  const { dir } = await terminalSession(f)
  await f.runtime.driver.listSessions()
  const s = f.runtime.sessions.get(ID)
  let release
  s.tail = new Promise((resolve) => { release = resolve })
  try {
    await f.send('Continue after the native summary disappears')
    assert.equal(s.running, false)
    assert.equal(s.queued, 1)
    await rm(join(dir, 'summary.json'))
    assert.equal((await f.runtime.driver.listSessions())[0].status, 'working')
    release()
    await until('continued prompt starts', () => f.acp.prompts.length === 1)
    assert.equal(s.running, true)
    assert.equal(s.queued, 0)
    assert.equal((await f.runtime.driver.listSessions())[0].status, 'working')
    f.acp.update({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Finished normally' } })
    f.acp.prompts[0].resolve({ stopReason: 'end_turn' })
    await s.tail
    assert.deepEqual(await f.runtime.driver.listSessions(), [])
    assert.equal(f.acp.calls.some((c) => c.method === 'session/close'), false)
  } finally { release() }
})

test('a terminal-held conversation stays listed after its native directory disappears', async (t) => {
  const f = await fixture(t)
  const { dir } = await terminalSession(f)
  await holdInTerminal(f)
  await f.runtime.driver.listSessions()
  await rm(dir, { recursive: true })
  assert.equal((await f.runtime.driver.listSessions())[0].session_id, ID)
  await assert.rejects(f.send('Still held'), /open in a terminal/)
  await writeFile(join(f.home, '.grok', 'active_sessions.json'), '[]')
  assert.deepEqual(await f.runtime.driver.listSessions(), [])
  assert.deepEqual(f.acp.calls, [])
})

test('assistant conversations are retained regardless of session kind or missing native files', async (t) => {
  for (const sessionKind of [undefined, 'headless', 'subagent']) await t.test(sessionKind || 'unmarked', async (t) => {
    const f = await fixture(t)
    const { dir } = await terminalSession(f, { cwd: f.cwd, sessionKind })
    assert.equal((await f.runtime.driver.listSessions())[0].session_id, ID)
    assert.equal(f.runtime.sessions.get(ID).assistant, true)
    await rm(dir, { recursive: true })
    assert.equal((await f.runtime.driver.listSessions())[0].session_id, ID)
    assert.deepEqual(f.acp.calls, [])
  })
})

test('assistant ownership from metadata protects a rediscovered conversation outside assistant directories', async (t) => {
  const f = await fixture(t)
  const { dir } = await terminalSession(f)
  await writeFile(join(dir, 'persona-runtime.json'), JSON.stringify({ version: 1, session_id: RAW, assistant: true }))
  await f.runtime.driver.listSessions()
  assert.equal(f.runtime.sessions.get(ID).assistant, true)
  await rm(dir, { recursive: true })
  assert.equal((await f.runtime.driver.listSessions())[0].session_id, ID)
})

test('a newly started conversation is retained before its native summary is written', async (t) => {
  const f = await fixture(t)
  await f.node.peer.call('session.start', { cwd: join(f.home, 'code', 'worktree') })
  assert.equal(f.runtime.sessions.get(ID).assistant, false)
  assert.equal((await f.runtime.driver.listSessions())[0].session_id, ID)
})

test('an unreadable summary is not treated as a deleted native session', async (t) => {
  const f = await fixture(t)
  const { dir } = await terminalSession(f)
  await f.runtime.driver.listSessions()
  await writeFile(join(dir, 'summary.json'), '{')
  assert.equal((await f.runtime.driver.listSessions())[0].session_id, ID)
  assert.deepEqual(f.acp.calls, [])
})

test('old terminal conversations are left out unless a terminal still holds them', async (t) => {
  const f = await fixture(t)
  await terminalSession(f, { updated: new Date(Date.now() - 30 * 24 * 60 * 60 * 1000).toISOString() })
  assert.equal((await f.runtime.driver.listSessions()).length, 0)
  await holdInTerminal(f)
  assert.equal((await f.runtime.driver.listSessions()).length, 1)
})

test('a conversation open in a terminal is read-only until that terminal exits, then continues through ACP', async (t) => {
  const f = await fixture(t)
  await terminalSession(f)
  await holdInTerminal(f)
  await f.runtime.driver.listSessions()
  for (const call of [() => f.send('From the phone'), () => f.node.peer.call('session.rename', { session_id: ID, title: 'New' }),
    () => f.node.peer.call('session.modes', { session_id: ID, permission_mode: 'plan' })]) {
    await assert.rejects(call(), /open in a terminal/)
  }
  assert.equal(f.acp.calls.some((c) => c.method === 'session/load'), false)
  // A process that no longer runs holds nothing.
  await holdInTerminal(f, RAW, 2 ** 22 + 12345)
  await f.send('From the phone')
  await until('continuation prompt', () => f.acp.prompts.length === 1)
  assert.equal(f.acp.calls.find((c) => c.method === 'session/load').params.cwd, join(f.home, 'code', 'project'))
  await f.done()
})

test('a watched terminal conversation receives the turns the terminal appends', async (t) => {
  const f = await fixture(t)
  const { dir } = await terminalSession(f)
  await holdInTerminal(f)
  await f.runtime.driver.listSessions()
  await f.node.peer.call('session.watch', { session_id: ID })
  const more = JSON.stringify({ method: 'session/update', params: { update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'Terminal answer' } } } }) + '\n'
  await writeFile(join(dir, 'updates.jsonl'), (await readFile(join(dir, 'updates.jsonl'), 'utf8')) + more)
  await f.runtime.driver.listSessions()
  await until('terminal answer pushed', () => f.node.events.some((e) => e.event === 'session.items' && e.params.session_id === ID &&
    e.params.items.some((i) => i.text === 'Terminal answer')))
})
