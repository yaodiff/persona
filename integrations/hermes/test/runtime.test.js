import assert from 'node:assert/strict'
import { once } from 'node:events'
import { access, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { HermesAcp } from '../lib/acp.js'
import { addItem, reconcileReplay, transcript } from '../lib/items.js'
import { createHermesRuntime } from '../lib/runtime.js'

const fixture = fileURLToPath(new URL('./acp-fixture.mjs', import.meta.url))
const sessionText = (sessionUpdate, text) => ({ sessionUpdate, content: { type: 'text', text } })

async function seedSession(storage, cwd, extra = {}, updates = []) {
  await writeFile(join(storage, 'native-seeded.json'), JSON.stringify({
    info: { sessionId: 'native-seeded', cwd, updatedAt: new Date().toISOString() }, updates, ...extra,
  }))
  return { session_id: 'hermes-native-seeded' }
}

async function setup(t) {
  const root = await mkdtemp(join(tmpdir(), 'hermes-test-'))
  const storage = join(root, 'profile')
  const cwd = join(root, 'workspace')
  await Promise.all([mkdir(storage), mkdir(cwd)])
  const node = await startFakeNode(t, { strict: true })
  let runtime
  t.after(async () => {
    await runtime?.stop()
    await rm(root, { recursive: true, force: true })
  })
  const restart = async () => {
    await runtime?.stop()
    const previous = node.hellos
    runtime = createHermesRuntime({
      bin: fixture, socketPath: node.path,
      env: { ...process.env, HERMES_HOME: storage, HERMES_TEST_STATE: storage }, retryMs: 20, scanMs: 25, idleMs: 140,
    })
    await runtime.start()
    await until('runtime hello', () => node.hellos > previous && node.peer)
  }
  await restart()
  return { node, cwd, storage, restart, call: (method, params) => node.peer.call(method, params) }
}

test('discovery only marks a saved identity claimed by that session as started by Persona', async (t) => {
  const { cwd, storage, restart, call } = await setup(t)
  const identities = join(storage, 'persona-acp')
  await mkdir(identities)
  for (const sessionId of ['native-a', 'native-b']) {
    await writeFile(join(storage, sessionId + '.json'), JSON.stringify({
      info: { sessionId, cwd, updatedAt: new Date().toISOString() },
      updates: [{ sessionUpdate: 'user_message_chunk', content: { type: 'text', text: sessionId } }],
    }))
    await writeFile(join(identities, sessionId + '.json'), JSON.stringify({ version: 1, id: 'persona-start' }))
  }
  await restart()
  const { rows } = await call('sessions.history', { since: new Date(0).toISOString() })
  const flags = Object.fromEntries(rows.map((row) => [row.session_id, row.started_by_persona]))
  assert.deepEqual(flags, { 'persona-start': true, 'hermes-native-b': false })
  const items = async (session_id) => (await call('session.items', { session_id })).items
  assert.equal((await items('persona-start'))[0].text, 'native-a')
  assert.equal((await items('hermes-native-b'))[0].text, 'native-b')
})

test('launch, stream, page, deduplicate and recover across node reconnect and companion restart', async (t) => {
  const { node, cwd, restart, call } = await setup(t)
  assert.equal(node.hello.runtime, 'hermes')
  assert.equal(node.hello.launchers[0].name, 'hermes')
  const start = { cwd, session_id: 'persona-start' }
  const { session } = await call('session.start', start)
  const session_id = session.session_id
  assert.equal(session_id, start.session_id)
  assert.equal((await call('session.start', start)).session.session_id, session_id)
  await call('session.watch', { session_id, since_seq: 0 })
  const first = { session_id, text: 'first', client_msg_id: 'message-one' }
  await Promise.all([call('session.send', first), call('session.send', first)])
  const read = (p = {}) => call('session.items', { session_id, ...p })
  await until('first reply', async () => (await read()).items.some((i) => i.text === 'echo: first'))
  const saved = await read()
  assert.equal(saved.items.filter((i) => i.role === 'user').length, 1)
  assert.equal(saved.items[0].client_msg_id, first.client_msg_id)
  assert.deepEqual((await call('session.item', { session_id, item_id: saved.items[1].id })).text, 'echo: first')
  const old = node.peer
  old.close()
  await until('node reconnect', () => node.peer !== old)
  await call('session.send', first)
  assert.equal((await read()).to_seq, saved.to_seq)

  const { session: second } = await call('session.start', { cwd, prompt: 'another workspace session', client_msg_id: 'initial' })
  await until('second reply', async () =>
    (await call('session.items', { session_id: second.session_id })).items.length === 2)
  await restart()
  assert.equal((await call('session.start', start)).session.session_id, session_id)
  const history = await call('sessions.history', { since: new Date(0).toISOString() })
  assert.equal(history.rows.find((row) => row.session_id === session_id).started_by_persona, true)
  const restored = await read()
  assert.notEqual(restored.history, saved.history)
  assert.deepEqual(restored.items.map((i) => i.text), ['first', 'echo: first'])
  assert.deepEqual((await read({ after_seq: 1 })).items.map((i) => i.text), ['echo: first'])
  assert.deepEqual((await read({ limit: 1, before_seq: 2 })).items.map((i) => i.text), ['first'])
  assert.equal((await call('session.items', { session_id: second.session_id })).items[0].text, 'another workspace session')
  await assert.rejects(call('session.items', { session_id: 'hermes-unknown' }), { code: 'not_found' })
  await call('session.send', { session_id, text: 'continued' })
  await until('continued reply', async () => (await read()).items.some((i) => i.text === 'echo: continued'))
})

test('interrupt settles a local wait while a shared native load is still pending', { timeout: 5_000 }, async (t) => {
  const { cwd, storage, call } = await setup(t)
  const hold = join(storage, 'load-hold')
  await writeFile(hold, '')
  const session = await seedSession(storage, cwd, { holdLoad: true })
  const cancelled = assert.rejects(call('session.send', { ...session, text: 'cancelled before load' }), {
    code: 'not_delivered',
  })
  await until('native load started', () => access(join(storage, 'load-started')).then(() => true, () => false))
  assert.deepEqual(await call('session.interrupt', session), { ok: true })
  await cancelled
  const next = call('session.send', { ...session, text: 'replacement' })
  await rm(hold)
  await next
  const read = async () => (await call('session.items', session)).items
  await until('replacement reply', async () => (await read()).at(-1)?.text === 'echo: replacement')
  assert.deepEqual((await read()).map((item) => item.text), ['replacement', 'echo: replacement'])
})

test('failed native load can retry even if stopping the failed worker rejects', async (t) => {
  const { cwd, storage, call } = await setup(t)
  const session = await seedSession(storage, cwd, { failLoad: true })
  const stop = HermesAcp.prototype.stop
  let failCleanup = true
  t.mock.method(HermesAcp.prototype, 'stop', function () {
    if (failCleanup) {
      failCleanup = false
      return Promise.reject(new Error('Could not stop the first worker'))
    }
    return stop.call(this)
  })
  await assert.rejects(call('session.items', session), /Hermes ACP session\/load failed/)
  assert.deepEqual((await call('session.items', session)).items, [])
  await call('session.send', { ...session, text: 'recovered' })
  await until('retry reply', async () => (await call('session.items', session)).items.at(-1)?.text === 'echo: recovered')
})

test('history comparison publishes client ID changes and preserves matching IDs through replay', () => {
  const item = { role: 'user', kind: 'text', text: 'input' }
  const old = transcript()
  addItem(old, item)
  const restored = transcript()
  addItem(restored, { ...item, client_msg_id: 'first-id' })
  assert.equal(reconcileReplay(old, restored), restored)
  const replay = transcript()
  addItem(replay, item)
  assert.equal(reconcileReplay(restored, replay), restored)
  assert.equal(replay.items[0].client_msg_id, 'first-id')
})

test('the final catch-up page and reconnect appends use numeric contiguous cursors', async (t) => {
  const { node, cwd, storage, call } = await setup(t)
  const updates = Array.from({ length: 252 }, (_, i) => [
    sessionText('user_message_chunk', `input ${i}`), sessionText('agent_message_chunk', `reply ${i}`),
  ]).flat()
  const session = await seedSession(storage, cwd, {}, updates)
  assert.equal((await call('session.items', session)).to_seq, 504)
  const first = await call('session.items', { ...session, after_seq: 1 })
  assert.equal(first.to_seq, 501)
  assert.equal(first.more_after, true)
  const last = await call('session.items', { ...session, after_seq: first.to_seq })
  assert.equal(last.to_seq, 504)
  assert.equal(last.more_after, false)
  const empty = await call('session.items', { ...session, after_seq: last.to_seq })
  assert.deepEqual(empty.items, [])
  assert.equal(empty.to_seq, 504)
  const old = node.peer
  old.close()
  await until('node reconnect', () => node.peer !== old)
  await call('session.watch', { ...session, since_seq: 1, history: last.history })
  await call('session.send', { ...session, text: 'after reconnect' })
  await until('append after catch-up', () => node.all('session.items').some((batch) => batch.to_seq === 506))
  const batches = node.all('session.items')
  assert.deepEqual(batches.slice(0, 2).map(({ from_seq, to_seq }) => [from_seq, to_seq]), [[1, 501], [501, 504]])
  assert.equal(batches[2].from_seq, 504)
  assert.ok(batches.every(({ to_seq }) => Number.isInteger(to_seq)))
})

test('permissions and cancellation are isolated to the active session', async (t) => {
  const { node, cwd, call } = await setup(t)
  const start = async () => ({ session_id: (await call('session.start', { cwd })).session.session_id })
  const [one, two] = await Promise.all([start(), start()])
  node.resolve = (ix) => ({ allow: ix.session_id === one.session_id })
  await Promise.all([one, two].map((s) => call('session.send', { ...s, text: 'permission' })))
  const replyText = async (s) => (await call('session.items', s)).items.filter((i) => i.role === 'assistant' && i.kind === 'text').at(-1)?.text
  await until('allow and deny', async () => await replyText(one) === 'once' && await replyText(two) === 'deny')
  assert.equal(new Set(node.asked.map((i) => i.id)).size, 2)

  for (const s of [one, two]) {
    await call('session.watch', { ...s, since_seq: 0 })
    await call('session.send', { ...s, text: 'hold' })
  }
  await until('two independent live streams', () => node.all('session.delta').filter((p) => p.text === 'streaming').length === 2)
  await assert.rejects(call('session.send', { ...one, text: 'must not enqueue' }), { code: 'turn_running' })
  assert.equal((await call('session.interrupt', one)).settled, true)
  assert.equal((await call('session.items', one)).items.at(-1).text, '已中断')
  assert.equal((await call('session.items', two)).items.at(-1).text, 'hold')
  assert.equal((await call('session.interrupt', two)).settled, true)
  node.resolve = () => new Promise(() => {})
  await call('session.send', { ...one, text: 'permission' })
  await until('permission held by the hub', () => node.asked.length === 3)
  assert.equal((await call('session.interrupt', one)).settled, true)
  assert.equal((await call('session.items', one)).items.at(-1).text, '已中断')
})

test('a crashed worker only ends its own turn and saved sessions can continue', async (t) => {
  const { cwd, call } = await setup(t)
  const { session } = await call('session.start', { cwd, prompt: 'save first', client_msg_id: 'saved-first' })
  const session_id = session.session_id
  const read = () => call('session.items', { session_id })
  await until('saved reply', async () => (await read()).items.at(-1)?.text === 'echo: save first')
  await call('session.send', { session_id, text: 'disconnect', client_msg_id: 'lost-turn' })
  await until('crash notice', async () => (await read()).items.at(-1)?.kind === 'notice')
  await call('session.send', { session_id, text: 'recovered' })
  await until('reply after recovery', async () => (await read()).items.at(-1)?.text === 'echo: recovered')
  const failed = ['save first', 'echo: save first', 'disconnect', '出错了', 'recovered', 'echo: recovered']
  assert.deepEqual((await read()).items.map((i) => i.text), failed)
  assert.equal((await read()).items[2].client_msg_id, 'lost-turn')
  await call('session.send', { session_id, text: 'saved disconnect', client_msg_id: 'saved-turn' })
  await until('second crash notice', async () => (await read()).items.at(-1)?.kind === 'notice')
  await call('session.send', { session_id, text: 'continue again' })
  await until('reply after second reload', async () => (await read()).items.at(-1)?.text === 'echo: continue again')
  const restored = await read()
  assert.deepEqual(restored.items.map((i) => i.text), [
    ...failed, 'saved disconnect', 'saved before exit', '出错了', 'continue again', 'echo: continue again',
  ])
  assert.equal(restored.items[6].client_msg_id, 'saved-turn')
  assert.equal(restored.items[0].client_msg_id, 'saved-first')
  await call('session.send', { session_id, text: 'saved disconnect', client_msg_id: 'saved-turn' })
  assert.deepEqual((await read()).items, restored.items)
})

test('missing Hermes executable rejects startup and cleans up without a close event', { timeout: 3_000 }, async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'hermes-missing-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const bin = join(root, 'missing-hermes')
  const worker = new HermesAcp({ bin })
  // Some spawn failures may never deliver close to the transport.
  worker.child.removeAllListeners('close')
  const failed = assert.rejects(worker.initialize(), /Hermes ACP disconnected/)
  await worker.stop()
  assert.equal(worker.closed, true)
  assert.equal(worker.pending.size, 0)
  await Promise.all([failed, worker.exited, worker.drained])
  const runtime = createHermesRuntime({ bin, env: { ...process.env, HERMES_HOME: root } })
  await assert.rejects(runtime.start(), /Hermes ACP disconnected/)
  await runtime.stop()
})

test('ended or destroyed stdin rejects new calls without leaving a pending request', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'hermes-input-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  for (const closeInput of ['end', 'destroy']) {
    const worker = new HermesAcp({ bin: fixture, cwd: root, env: { ...process.env, HERMES_TEST_STATE: root } })
    t.after(() => worker.stop())
    await worker.initialize()
    worker.child.stdin[closeInput]()
    await assert.rejects(worker.call('session/list', {}, 0), /Hermes ACP disconnected/)
    assert.equal(worker.pending.size, 0)
    await worker.stop()
  }
})

test('an asynchronous pipe write failure rejects a call before worker exit', {
  skip: process.platform === 'win32', timeout: 5_000,
}, async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'hermes-pipe-'))
  const worker = new HermesAcp({
    bin: fixture, cwd: root,
    env: { ...process.env, HERMES_TEST_STATE: root, HERMES_TEST_CLOSE_STDIN_AFTER_LIST: '1' },
  })
  t.after(async () => {
    await worker.stop()
    await rm(root, { recursive: true, force: true })
  })
  await worker.initialize()
  assert.deepEqual(await worker.call('session/list', {}), { sessions: [] })
  assert.equal(worker.child.stdin.writable, true)
  await assert.rejects(worker.call('session/list', {}, 0), /Hermes ACP disconnected/)
  assert.equal(worker.pending.size, 0)
  assert.equal(worker.child.exitCode, null)
  assert.equal(worker.child.signalCode, null)
  assert.equal(worker.writable, false)
})

test('a failed prompt write ends the turn and retains its failure across reload', {
  skip: process.platform === 'win32',
}, async (t) => {
  const { cwd, call } = await setup(t)
  const { session } = await call('session.start', { cwd, prompt: 'close input' })
  const session_id = session.session_id
  const read = async () => (await call('session.items', { session_id })).items
  await until('last successful reply', async () => (await read()).at(-1)?.text === 'input closed')
  await call('session.send', { session_id, text: 'failed write', client_msg_id: 'failed-write' })
  await until('failed write notice', async () => (await read()).at(-1)?.kind === 'notice')
  await until('replacement worker accepts a prompt', async () => {
    try {
      return await call('session.send', { session_id, text: 'recovered' })
    } catch (error) {
      if (error.code !== 'not_delivered') throw error
      return false
    }
  })
  await until('replacement worker reply', async () => (await read()).at(-1)?.text === 'echo: recovered')
  const items = await read()
  assert.deepEqual(items.map((item) => item.text), [
    'close input', 'input closed', 'failed write', '出错了', 'recovered', 'echo: recovered',
  ])
  assert.equal(items[2].client_msg_id, 'failed-write')
})

test('normal permission requests and prompt replies reach their respective handlers', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'hermes-permission-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const permissions = []
  const worker = new HermesAcp({
    bin: fixture, cwd: root, env: { ...process.env, HERMES_TEST_STATE: root },
    onPermission: (params) => {
      permissions.push(params)
      return { outcome: 'selected', optionId: 'once' }
    },
  })
  t.after(() => worker.stop())
  await worker.initialize()
  const { sessionId } = await worker.call('session/new', { cwd: root, mcpServers: [] })
  const reply = await worker.call('session/prompt', { sessionId, prompt: [{ type: 'text', text: 'permission' }] })
  assert.equal(reply.stopReason, 'end_turn')
  assert.equal(permissions.length, 1)
  assert.equal(permissions[0].sessionId, sessionId)
})

test('worker exit drains its final response and stop settles without a close event', { timeout: 4_000 }, async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'hermes-exit-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const worker = new HermesAcp({
    bin: fixture, cwd: root,
    env: { ...process.env, HERMES_TEST_STATE: root, HERMES_TEST_EXIT_AFTER_LIST: '1' },
  })
  await worker.initialize()
  worker.child.removeAllListeners('close')
  worker.child.stdout.pause()
  const exit = once(worker.child, 'exit')
  const reply = worker.call('session/list', {})
  const stop = worker.stop()
  // Defer reads until after exit to exercise data still buffered in stdout.
  assert.deepEqual(await exit, [0, null])
  worker.child.stdout.resume()
  assert.deepEqual(await reply, { sessions: [] })
  await stop
  await worker.drained
  assert.equal(worker.closed, true)
  assert.equal(worker.child.stdout.destroyed, true)
  assert.equal(worker.child.stderr.destroyed, true)
})

test('consecutive Hermes message IDs live-stream the full text and clear it at the turn boundary', async (t) => {
  const { node, cwd, call } = await setup(t)
  const { session } = await call('session.start', { cwd })
  const params = { session_id: session.session_id }
  await call('session.watch', { ...params, since_seq: 0 })
  await call('session.send', { ...params, text: 'segmented' })
  const deltas = () => node.all('session.delta').map(({ text }) => text)
  await until('full replacement delta', () => deltas().includes('first second'))
  assert.equal(deltas().includes('second'), false)
  await call('session.interrupt', params)
  await until('cleared stream', () => deltas().at(-1) === '')
  await call('session.send', { ...params, text: 'hold' })
  await until('next turn starts a fresh stream', () => deltas().at(-1) === 'streaming')
  await call('session.interrupt', params)
})
