import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { test } from 'node:test'
import { createRuntime, Peer, RpcError } from '../index.js'
import { startFakeNode, until } from '../testing/fake-node.js'

const params = { session_id: 's1', task: 'task-1', wait_id: 'first', line: ' private\tanswer ' }

async function connect(t, driver, options) {
  const node = await startFakeNode(t, options)
  const logs = []
  const runtime = createRuntime({ start: ({ session_id }) => ({ session: { session_id } }),
    listSessions: () => [{ session_id: params.session_id, waiting_inputs: [{ task: params.task, wait_id: params.wait_id }] }], ...driver }, {
    runtime: 'toy', socketPath: node.path, refreshMs: 0, retryMs: 20, pushDelayMs: 0, log: message => logs.push(message),
  })
  t.after(() => runtime.stop())
  await runtime.start()
  await node.ready
  await until('runtime ready', () => runtime.ready)
  return { node, runtime, logs }
}

async function startBound(node, id = params.session_id, binding = randomUUID()) {
  await node.peer.call('session.start', { session_id: id, input_binding: binding })
  await until('bound wait', () => node.last('runtime.sessions')?.sessions.some(row => row.input_binding === binding))
  return binding
}

function refused(code) {
  return error => {
    assert.equal(error.code, code)
    assert.equal(error.message, code)
    assert.equal(error.data, undefined)
    return true
  }
}

test('waiting input survives snapshots, and concurrent and stale answers cannot type twice', async t => {
  const prompt = () => ({ task: params.task, wait_id: randomUUID(), command: 'login', prompt: 'Code?', output: 'Ready', masked: true })
  let waiting = prompt()
  const first = waiting
  const typed = []
  const claimed = Promise.withResolvers()
  const confirmation = Promise.withResolvers()
  const { node, runtime } = await connect(t, {
    listSessions: () => [{ session_id: params.session_id, waiting_inputs: waiting ? [waiting] : [] }],
    async input({ session_id, task, wait_id, line }) {
      if (session_id !== params.session_id || !waiting || task !== waiting.task || wait_id !== waiting.wait_id) {
        throw new RpcError('obsolete', 'prompt_changed')
      }
      waiting = undefined
      claimed.resolve()
      await confirmation.promise
      typed.push(line)
      return { typed: true, private: line }
    },
  })
  assert.equal(node.hello.caps.input, true)
  await startBound(node)
  const waits = () => node.last('runtime.sessions')?.sessions[0].waiting_inputs
  await until('first wait', waits)
  assert.deepEqual(waits(), [first])
  const snapshots = node.all('runtime.sessions').length
  runtime.sessionsChanged(0)
  await until('second snapshot', () => node.all('runtime.sessions').length > snapshots)
  assert.deepEqual(waits(), [first])

  const answer = { ...params, wait_id: first.wait_id }
  let acknowledged = false
  const pending = node.input(answer).then(result => { acknowledged = true; return result })
  await claimed.promise
  await assert.rejects(node.input(answer), refused('prompt_changed'))
  assert.equal(acknowledged, false)
  confirmation.resolve()
  assert.deepEqual(await pending, { typed: true })
  await until('cleared wait', () => !waits())

  waiting = prompt()
  runtime.sessionsChanged(0)
  await until('same prompt asked again', () => waits()?.[0]?.wait_id === waiting.wait_id)
  assert.notEqual(waiting.wait_id, first.wait_id)
  await assert.rejects(node.input(answer), refused('prompt_changed'))
  assert.deepEqual(typed, [params.line])
  assert.deepEqual(await node.input({ ...answer, wait_id: waiting.wait_id, line: 'second' }), { typed: true })
  assert.deepEqual(typed, [params.line, 'second'])
})

test('input preserves only definitive refusal codes and explicit confirmation', async t => {
  let respond
  const { node, logs } = await connect(t, { input: request => respond(request) })
  await startBound(node)
  for (const code of ['prompt_changed', 'command_input_refused', 'command_input_unreachable', 'command_input_line', 'command_input_unconfirmed', params.line, undefined]) {
    respond = async ({ line }) => { throw new RpcError(line, code, { detail: line }) }
    await assert.rejects(node.input(params), refused(code && code !== params.line ? code : 'command_input_unconfirmed'))
  }
  const unreadable = { get typed() { throw new RpcError(params.line, 'prompt_changed') } }
  for (const result of [unreadable, undefined, null, true, {}, { ok: true }, { typed: false }, { typed: 'true' }]) {
    respond = () => result
    await assert.rejects(node.input(params), refused('command_input_unconfirmed'))
  }
  assert.ok(logs.every(message => !message.includes(params.line)))
})

test('input rejects malformed requests before calling the driver and requires an advertised handler', async t => {
  let calls = 0
  const { node } = await connect(t, { input() { calls++; return { typed: true } } })
  await startBound(node)
  for (const line of [null, '', 'a\nb', 'a\r', '\x1b', '\u0085', '\u2028', '\ud800', '界'.repeat(1366)]) {
    await assert.rejects(node.input({ ...params, line }), refused('command_input_line'))
  }
  for (const key of ['session_id', 'task', 'wait_id']) {
    await assert.rejects(node.input({ ...params, [key]: undefined }), refused('prompt_changed'))
  }
  assert.equal(calls, 0)
  const { node: legacy } = await connect(t, {
    input: true, listSessions: () => [{ session_id: 's1', waiting_inputs: [] }],
  })
  await until('list without driver input', () => legacy.last('runtime.sessions'))
  assert.deepEqual(legacy.last('runtime.sessions').sessions, [{ session_id: 's1' }])
  assert.equal(legacy.hello.caps.input, undefined)
  await assert.rejects(legacy.input(params), refused('command_input_unreachable'))
})

test('a node without input support sees ordinary session rows', async t => {
  const { node } = await connect(t, {
    listSessions: () => [{ session_id: 's1', waiting_inputs: [{ task: 'local-task' }] }],
    input: () => assert.fail('input was not negotiated'),
  }, { features: [] })
  await until('ordinary session row', () => node.last('runtime.sessions'))
  assert.deepEqual(node.last('runtime.sessions').sessions, [{ session_id: 's1' }])
  await assert.rejects(node.input(params), refused('command_input_unreachable'))
})

test('start, fork and history replies strip private fields from reused live rows', async t => {
  const ordinary = { session_id: 's1', status: 'working', updated_at: '2026-01-01T00:00:00Z' }
  const live = Object.freeze({ ...ordinary, input_binding: 'driver-forgery', waiting_inputs: [{ task: 'private-task' }] })
  const history = { rows: [live], complete: false, next_cursor: 'next' }
  const query = { since: '2025-01-01T00:00:00Z', until: '2027-01-01T00:00:00Z' }
  const { node, runtime } = await connect(t, {
    listSessions: () => [live], listHistory: () => history,
    start: () => ({ session: live, resumed: true }), fork: p => p.flat ? live : { session_id: live.session_id, session: live }, input: () => ({ typed: true }),
  })
  assert.deepEqual(await node.peer.call('sessions.history', query), { ...history, rows: [ordinary] })
  assert.deepEqual(runtime.history([live], query).rows, [ordinary])
  assert.deepEqual(await node.peer.call('session.start', {}), { session: ordinary, resumed: true })
  assert.deepEqual(await node.peer.call('session.fork', {}), { session_id: 's1', session: ordinary })
  assert.deepEqual(await node.peer.call('session.fork', { flat: true }), ordinary)
  const binding = await startBound(node)
  assert.deepEqual(node.last('runtime.sessions').sessions, [{ ...live, input_binding: binding }])
})

for (const finishBeforeHello of [true, false]) {
  test(`a replaced peer's list finishing ${finishBeforeHello ? 'before' : 'after'} the next hello stays private`, async t => {
    const requests = []
    const call = Peer.prototype.call
    t.mock.method(Peer.prototype, 'call', function (method, ...args) {
      if (method === 'runtime.hello') requests.push(this.onRequest)
      return call.call(this, method, ...args)
    })
    const oldList = Promise.withResolvers()
    const handshake = Promise.withResolvers()
    const preReadyList = Promise.withResolvers()
    const row = session_id => ({ session_id, waiting_inputs: [{ task: session_id }] })
    let list = () => [row('current')]
    let typed = 0
    const { node, runtime } = await connect(t, {
      listSessions: () => list(), input() { typed++; return { typed: true } },
      items: () => ({ to_seq: 0, items: [] }),
    })
    await until('initial inventory', () => node.last('runtime.sessions'))
    const binding = await startBound(node, 'current')
    node.events.length = 0
    list = () => oldList.promise
    const stale = runtime.pushNow()
    node.holdHello = handshake.promise
    if (finishBeforeHello) {
      runtime.stop()
      await runtime.start()
    } else {
      node.peer.close(new Error('node restarting'))
    }
    await until('replacement hello', () => node.hellos === 2)
    assert.equal(runtime.ready, false)
    assert.equal(runtime.nodeHas('input'), false)
    await assert.rejects(requests[1]('session.input', params), refused('command_input_unreachable'))

    const finishOld = async () => {
      oldList.resolve([row('obsolete')])
      await stale
    }
    if (finishBeforeHello) await finishOld()
    list = () => preReadyList.promise
    const early = runtime.pushNow()
    handshake.resolve()
    await until('replacement ready', () => runtime.ready)
    assert.equal(typed, 0)
    list = () => [row('current')]
    preReadyList.resolve([{ ...row('current'), title: 'before-ready' }])
    await early
    await until('fresh negotiated inventory', () => node.last('runtime.sessions')?.sessions[0]?.input_binding === binding)
    if (!finishBeforeHello) await finishOld()
    // The reply travels behind any inventory already written on this connection.
    await node.peer.call('session.items', { session_id: 'current' })
    const rows = node.all('runtime.sessions').flatMap(event => event.sessions)
    assert.ok(!rows.some(session => session.session_id === 'obsolete'))
    assert.deepEqual(rows.find(session => session.title === 'before-ready'), { session_id: 'current', title: 'before-ready' })
    assert.deepEqual(rows.at(-1), { ...row('current'), input_binding: binding })
    const current = { ...params, session_id: 'current' }
    await assert.rejects(requests[0]('session.input', current), refused('command_input_unreachable'))
    assert.deepEqual(await node.input(current), { typed: true })
    assert.equal(typed, 1)
  })
}

test('only a matching completed start binds, and replacing its peer discards an in-flight start', async t => {
  const row = { session_id: params.session_id, input_binding: 'forged', waiting_inputs: [{ task: params.task }] }
  const started = Promise.withResolvers()
  const finish = Promise.withResolvers()
  let result = () => ({ session: row })
  const { node, runtime, logs } = await connect(t, {
    start(request) {
      assert.deepEqual(request, { session_id: params.session_id })
      return result()
    },
    listSessions: () => [row],
    input: () => assert.fail('unbound session received input'),
  })
  const binding = randomUUID()
  const request = { session_id: params.session_id, input_binding: binding }
  const start = () => node.peer.call('session.start', request)
  result = () => { throw new Error('start failed') }
  await assert.rejects(start(), /start failed/)
  result = () => ({ session: { ...row, session_id: 'other' } })
  assert.deepEqual(await start(), { session: { session_id: 'other' } })
  result = () => row
  assert.deepEqual(await start(), { session_id: params.session_id })
  result = () => { started.resolve(); return finish.promise }
  const pending = start()
  await started.promise
  await assert.rejects(node.input(params), refused('command_input_unreachable'))
  node.peer.close()
  await assert.rejects(pending, error => error.code === 'closed')
  await until('reconnected', () => node.hellos === 2 && runtime.ready)
  finish.resolve({ session: row })
  await runtime.pushNow()
  await node.peer.call('session.unwatch', params)
  await assert.rejects(node.input(params), refused('command_input_unreachable'))
  assert.ok(node.all('runtime.sessions').length > 0)
  for (const snapshot of node.all('runtime.sessions')) assert.deepEqual(snapshot.sessions, [{ session_id: params.session_id }])
  assert.ok(logs.every(message => !message.includes(binding)))
})

test('withdrawn rows suppress waits before pending status overrides, and a successful close releases the binding', async t => {
  const row = { session_id: params.session_id, waiting_inputs: [{ task: params.task, wait_id: params.wait_id }] }
  let rows = [row]
  let closeFails = true
  const { node, runtime } = await connect(t, {
    listSessions: () => rows,
    close() { if (closeFails) throw new Error('could not close') },
    input: () => ({ typed: true }),
  })
  const binding = await startBound(node)
  const release = runtime.waitingOnUser(params.session_id)
  async function snapshot(next) {
    rows = next
    await runtime.pushNow()
    await node.peer.call('session.unwatch', params)
    return node.last('runtime.sessions').sessions
  }
  for (const withdrawn of [[], ...[{ status: 'interrupted' }, { status: 'offline' }, { archived: true },
    { status: 'archived' }].map(fields => [{ ...row, ...fields }])]) {
    const list = await snapshot(withdrawn)
    assert.ok(list.every(s => s.status === 'blocked' && !('waiting_inputs' in s) && !('input_binding' in s)))
    await assert.rejects(node.input(params), refused('command_input_unreachable'))
    assert.deepEqual(await snapshot([row]), [{ ...row, status: 'blocked', input_binding: binding }])
  }
  await assert.rejects(node.peer.call('session.close', params), /could not close/)
  assert.deepEqual(await node.input(params), { typed: true })
  closeFails = false
  await node.peer.call('session.close', params)
  const hidden = [{ session_id: params.session_id, status: 'blocked' }]
  assert.deepEqual(await snapshot([row]), hidden)
  await assert.rejects(node.input(params), refused('command_input_unreachable'))
  for (const deletion of [{ deleted: true }, { status: 'deleted' }]) {
    await startBound(node)
    const [deleted] = await snapshot([{ ...row, ...deletion }])
    assert.ok(!('waiting_inputs' in deleted) && !('input_binding' in deleted))
    assert.deepEqual(await snapshot([row]), hidden)
  }
  await startBound(node)
  await snapshot([])
  const now = Date.now()
  t.mock.method(Date, 'now', () => now + 24 * 60 * 60 * 1000)
  await snapshot([])
  assert.deepEqual(await snapshot([row]), hidden)
  release()
})

test('reconnecting to an old node drops previously negotiated input', async t => {
  const options = { features: ['input'] }
  const { node, runtime } = await connect(t, { input: () => ({ typed: true }) }, options)
  await startBound(node)
  options.features = []
  node.events.length = 0
  node.peer.close()
  await until('old node inventory', () => node.hellos === 2 && runtime.ready && node.last('runtime.sessions'))
  assert.deepEqual(node.last('runtime.sessions').sessions, [{ session_id: params.session_id }])
  await assert.rejects(node.input(params), refused('command_input_unreachable'))
})

test('a start received during hello keeps its binding after negotiation', async t => {
  const { node, runtime } = await connect(t, { input: () => ({ typed: true }) })
  const handshake = Promise.withResolvers()
  node.holdHello = handshake.promise
  node.peer.close()
  await until('next hello pending', () => node.hellos === 2)
  assert.equal(runtime.ready, false)
  const connecting = [...node.peers].find(p => !p.closed)
  const binding = randomUUID()
  const start = connecting.call('session.start', { session_id: params.session_id, input_binding: binding })
  // The unwatch reply proves the preceding start request arrived before hello completed.
  await connecting.call('session.unwatch', params)
  handshake.resolve()
  await start
  await until('wait bound after hello', () => node.last('runtime.sessions')?.sessions[0]?.input_binding === binding)
  assert.deepEqual(await node.input(params), { typed: true })
})
