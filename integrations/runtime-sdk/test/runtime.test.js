import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createRuntime, interactionId, light, PAGE_TEXT, Peer, RpcError } from '../index.js'
import { startFakeNode, until } from '../testing/fake-node.js'
import { createEchoRuntime } from '../examples/echo-runtime.mjs'

const minimal = (extra = {}) => ({ listSessions: async () => [{ session_id: 's1', status: 'idle' }], items: async () => ({ items: [], to_seq: 0 }), ...extra })

test('hello names the runtime and declares only the caps whose driver methods exist', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ send: async () => {} }), { runtime: 'toy', name: 'Toy', agent: 'claude', launcher: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  assert.equal(n.hello.runtime, 'toy')
  assert.equal(n.hello.name, 'Toy')
  assert.equal(n.hello.agent, 'claude')
  assert.deepEqual(n.hello.caps, { start: false, send: true, interrupt: false, rename: false, close: false, assistant: false,
    queue_edit: false, queue_send_now: false })
  assert.equal(n.hello.launcher, undefined, 'no launcher without start')
  await until('list', () => n.last('runtime.sessions'))
  await n.peer.call('session.send', { session_id: 's1', text: 'x' })
  await assert.rejects(n.peer.call('session.interrupt', { session_id: 's1' }), /unsupported/)
  for (const method of ['session.queue.edit', 'session.queue.send_now']) {
    await assert.rejects(n.peer.call(method, { session_id: 's1' }), { code: 'unsupported' })
  }
  await assert.rejects(n.peer.call('session.fork', { session_id: 's1' }), (error) => error.code === 'unsupported')
  assert.throws(() => createRuntime(minimal(), {}), /runtime id/)
})

test('queue capabilities forward edits and steering, and publish both changes', async (t) => {
  const node = await startFakeNode(t)
  const session_id = 's1'
  let queued = ['first', 'second']
  const rt = createRuntime(minimal({
    listSessions: async () => [{ session_id, status: 'working', queued }],
    editQueue: async (params) => {
      assert.deepEqual(params, { session_id, keep: [1] })
      const queue = queued
      queued = params.keep.map((index) => queue[index])
      return { queue }
    },
    sendNow: async (params) => {
      assert.deepEqual(params, { session_id })
      queued = []
    },
  }), { runtime: 'toy', socketPath: node.path, pushDelayMs: 5, refreshMs: 0 })
  t.after(() => rt.stop())
  await rt.start()
  await until('initial queue published', () => node.last('runtime.sessions')?.sessions[0]?.queued?.length === 2)
  assert.equal(node.hello.caps.queue_edit, true)
  assert.equal(node.hello.caps.queue_send_now, true)
  assert.deepEqual(await node.peer.call('session.queue.edit', { session_id, keep: [1] }), { queue: ['first', 'second'] })
  await until('edit published', () => node.last('runtime.sessions').sessions[0].queued?.length === 1)
  assert.deepEqual(await node.peer.call('session.queue.send_now', { session_id }), { sent: true })
  await until('send-now published', () => node.last('runtime.sessions').sessions[0].queued?.length === 0)
})

test('fork advertises native support and returns the driver response', async (t) => {
  const node = await startFakeNode(t)
  const params = { session_id: 's1', at_item_id: '12#0' }
  const rt = createRuntime(minimal({ fork: async (input) => {
    assert.deepEqual(input, params)
    return { session_id: 's2' }
  } }), { runtime: 'toy', socketPath: node.path })
  t.after(() => rt.stop())
  rt.start()
  await node.ready
  assert.equal(node.hello.caps.fork, true)
  assert.deepEqual(await node.peer.call('session.fork', params), { session_id: 's2' })
})

test('assistant worker PID snapshots report updates and replay only live workers after reconnect', async (t) => {
  const node = await startFakeNode(t, { features: ['assistant_pids'] })
  const rt = createRuntime(minimal({ start: async () => ({}), send: async () => {} }), {
    runtime: 'toy', assistant: true, socketPath: node.path, retryMs: 20, refreshMs: 0,
  })
  t.after(() => rt.stop())
  assert.equal(rt.assistantPid('s1', 110), false)
  assert.throws(() => rt.assistantPid('s1', -1), TypeError)
  await rt.start()
  await until('first PID report', () => node.last('runtime.assistant_pids')?.pids.s1 === 110)
  rt.assistantPid('s2', 220)
  rt.assistantPid('s1', 0)
  await until('s1 removed', () => node.last('runtime.assistant_pids')?.pids.s2 === 220 && !node.last('runtime.assistant_pids').pids.s1)
  const peer = node.peer
  const reports = node.all('runtime.assistant_pids').length
  peer.close()
  await until('replayed on new connection', () => node.peer !== peer && node.all('runtime.assistant_pids').length > reports)
  assert.deepEqual(node.last('runtime.assistant_pids'), { pids: { s2: 220 } })
  rt.assistantPid('s2', 0)
  await until('empty snapshot', () => Object.keys(node.last('runtime.assistant_pids').pids).length === 0)
})

test('the last assistant worker exiting while the hub is down replays an empty snapshot when it returns', async (t) => {
  const node = await startFakeNode(t, { features: ['assistant_pids'] })
  const rt = createRuntime(minimal({ start: async () => ({}), send: async () => {} }), {
    runtime: 'toy', assistant: true, socketPath: node.path, retryMs: 20, refreshMs: 0,
  })
  t.after(() => rt.stop())
  await rt.start()
  await until('connected and listed', () => rt.hubConnected && node.last('runtime.sessions'))
  rt.assistantPid('s1', 110)
  await until('worker reported', () => node.last('runtime.assistant_pids')?.pids.s1 === 110)
  node.setHub(false)
  await until('hub gone', () => !rt.hubConnected)
  rt.assistantPid('s1', 0)
  await until('worker removed', () => Object.keys(node.last('runtime.assistant_pids').pids).length === 0)
  const reports = node.all('runtime.assistant_pids').length
  const peer = node.peer
  node.setHub(true)
  await until('snapshot replayed', () => node.all('runtime.assistant_pids').length > reports)
  assert.equal(node.peer, peer)
  assert.deepEqual(node.last('runtime.assistant_pids'), { pids: {} })
  node.setHub(false)
  await until('hub gone again', () => !rt.hubConnected)
  const beforeReconnect = node.all('runtime.assistant_pids').length
  peer.close()
  await until('next socket sends its empty snapshot', () => node.peer !== peer && node.all('runtime.assistant_pids').length > beforeReconnect)
  assert.deepEqual(node.last('runtime.assistant_pids'), { pids: {} })
})

for (const [assistant, features] of [[true, []], [false, ['assistant_pids']]]) {
  test(`assistant PID reports require both sides to opt in (assistant=${assistant})`, async (t) => {
    const node = await startFakeNode(t, { features })
    const rt = createRuntime(minimal({ start: async () => ({}), send: async () => {} }), {
      runtime: 'toy', assistant, socketPath: node.path, refreshMs: 0,
    })
    t.after(() => rt.stop())
    await rt.start()
    await until('runtime listed', () => node.last('runtime.sessions'))
    assert.equal(rt.assistantPid('s1', 110), false)
    await rt.pushNow()
    assert.equal(node.last('runtime.assistant_pids'), undefined)
  })
}

test('a list carries the transcripts the runtime names as its own though it does not list them', async (t) => {
  const n = await startFakeNode(t)
  let past = ['aaaaaaaa-0000-4000-8000-0000000000a1']
  const rt = createRuntime(minimal({ unlistedTranscripts: async () => past }), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('the list', () => n.last('runtime.sessions'))
  assert.deepEqual(n.last('runtime.sessions'), { sessions: [{ session_id: 's1', status: 'idle' }], unlisted_transcripts: past })
  past = []
  rt.sessionsChanged()
  await until('none any more: the list says so', () => n.all('runtime.sessions').length >= 2 && n.last('runtime.sessions').unlisted_transcripts?.length === 0)
  // It cannot say now (its archive is being read again): the field is left out, and the node keeps what it was told.
  past = undefined
  rt.sessionsChanged()
  await until('listed again', () => n.all('runtime.sessions').length >= 3)
  assert.deepEqual(n.last('runtime.sessions').unlisted_transcripts, [], 'what it named last')
  // One that has never said sends the rows alone.
  const q = await startFakeNode(t)
  const unsure = createRuntime(minimal({ unlistedTranscripts: () => undefined }), { runtime: 'toy', socketPath: q.path, retryMs: 20 })
  t.after(() => unsure.stop())
  unsure.start()
  await until('its list', () => q.last('runtime.sessions'))
  assert.deepEqual(q.last('runtime.sessions'), { sessions: [{ session_id: 's1', status: 'idle' }] })
  // A runtime without the method sends the rows alone, as before.
  const m = await startFakeNode(t)
  const plain = createRuntime(minimal(), { runtime: 'toy', socketPath: m.path, retryMs: 20 })
  t.after(() => plain.stop())
  plain.start()
  await until('its list', () => m.last('runtime.sessions'))
  assert.deepEqual(m.last('runtime.sessions'), { sessions: [{ session_id: 's1', status: 'idle' }] })
})

test('a driver that cannot name its transcripts now still lists its sessions, with the ones it named last', async (t) => {
  const n = await startFakeNode(t)
  const past = ['aaaaaaaa-0000-4000-8000-0000000000a1']
  let fail = false
  const logs = []
  const unlistedTranscripts = () => {
    if (fail) throw new Error('boom')
    return [...past, '', 7] // only ids go out
  }
  const rt = createRuntime(minimal({ unlistedTranscripts }), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5, refreshMs: 0, log: (m) => logs.push(m) })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('the list', () => n.last('runtime.sessions'))
  assert.deepEqual(n.last('runtime.sessions').unlisted_transcripts, past)
  fail = true
  rt.sessionsChanged()
  await until('listed again', () => n.all('runtime.sessions').length >= 2)
  assert.deepEqual(n.last('runtime.sessions'), { sessions: [{ session_id: 's1', status: 'idle' }], unlisted_transcripts: past })
  assert.ok(logs.some((l) => /unlisted transcripts: boom/.test(l)), 'and the log says why')
})

test('a card for a session new in the list waits for that list, however long the driver takes to name its transcripts', async (t) => {
  const n = await startFakeNode(t, { strict: true })
  n.resolve = () => ({ allow: true })
  let rows = [{ session_id: 's1', status: 'idle' }]
  let naming = 0
  let named = () => {}
  const unlistedTranscripts = async () => {
    if (++naming > 1) await new Promise((r) => { named = r }) // the second list on: as slow as the test says
    return []
  }
  const rt = createRuntime(minimal({ listSessions: async () => rows, unlistedTranscripts }), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('the first list', () => n.last('runtime.sessions'))
  rows = [...rows, { session_id: 'new', status: 'working' }]
  rt.sessionsChanged()
  await until('the driver asked for its transcripts', () => naming === 2)
  // The list with the new session is read but not sent yet: its card must not be sent now (the node refuses a card for
  // a session it has not been told of).
  const asking = rt.ask({ id: 'toy-new', kind: 'permission', session_id: 'new', tool_name: 'Bash', tool_input: {} })
  await new Promise((r) => setTimeout(r, 30))
  assert.equal(n.asked.length + n.refused.length, 0, 'held until the list is out')
  const release = setInterval(() => named(), 5) // every list from here on
  t.after(() => clearInterval(release))
  assert.deepEqual(await asking, { allow: true })
  assert.equal(n.refused.length, 0)
})

test('appended content goes out as continuous batches, and a gap says where it starts', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ cursor: async () => 10 }), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  rt.append('s1', { items: [{ id: 'x' }], fromSeq: 9, toSeq: 10 }) // not watched: nothing is pushed
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  for (const seq of [11, 12]) rt.append('s1', { items: [{ id: `i${seq}`, seq }], fromSeq: seq - 1, toSeq: seq, history: 'h' })
  rt.append('s1', { items: [], fromSeq: 12, toSeq: 13, history: 'h' }) // a step the chat does not show still moves the seq
  await until('first batch', () => n.all('session.items').length === 1)
  assert.deepEqual(n.all('session.items')[0], { session_id: 's1', items: [{ id: 'i11', seq: 11 }, { id: 'i12', seq: 12 }], from_seq: 10, to_seq: 13, history: 'h' })
  rt.append('s1', { items: [{ id: 'i20', seq: 20 }], fromSeq: 19, toSeq: 20, history: 'h' })
  await until('gap batch', () => n.all('session.items').length === 2)
  assert.equal(n.all('session.items')[1].from_seq, 19)
})

test('content rewritten under another history goes out even where its seq did not grow, and goes on from there', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ items: async () => ({ items: [], history: 'h1', to_seq: 10 }) }), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  rt.append('s1', { items: [], toSeq: 10, history: 'h2' }) // another branch of as many entries
  await until('the same seq, another history', () => n.all('session.items').length === 1)
  assert.deepEqual(n.all('session.items')[0], { session_id: 's1', items: [], to_seq: 10, history: 'h2' })
  rt.append('s1', { items: [], toSeq: 4, history: 'h3' }) // a rewind: a shorter one
  await until('a lower seq, another history', () => n.all('session.items').length === 2)
  assert.deepEqual(n.all('session.items')[1], { session_id: 's1', items: [], to_seq: 4, history: 'h3' })
  rt.append('s1', { items: [{ id: 'i5', seq: 5 }], fromSeq: 4, toSeq: 5, history: 'h3' }) // below the old 10, still new
  await until('goes on from the rewrite', () => n.all('session.items').length === 3)
  assert.deepEqual(n.all('session.items')[2], { session_id: 's1', items: [{ id: 'i5', seq: 5 }], from_seq: 4, to_seq: 5, history: 'h3' })
  rt.append('s1', { items: [], fromSeq: 4, toSeq: 5, history: 'h3' }) // the same again: already pushed
  await new Promise((r) => setTimeout(r, 100))
  assert.equal(n.all('session.items').length, 3)
})

test('a wake batch reaches a watcher before the first live text of the new exchange', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ items: async () => ({ items: [], history: 'h', to_seq: 0 }) }), { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  rt.append('s1', { items: [{ id: 'reply#0', seq: 1, kind: 'text' }], fromSeq: 0, toSeq: 1, history: 'h' })
  rt.append('s1', { items: [{ id: 'reply#-1', seq: 2, kind: 'notice', wake: true }], fromSeq: 1, toSeq: 2, history: 'h' })
  rt.stream('s1', 'Checking again.')
  await until('new exchange starts streaming', () => n.last('session.delta')?.text === 'Checking again.')
  const events = n.events.filter(entry => entry.event === 'session.items' || entry.event === 'session.delta')
  assert.deepEqual(events.map(entry => entry.event), ['session.items', 'session.delta'])
  assert.deepEqual(events[0].params.items.map(item => item.id), ['reply#0', 'reply#-1'])
})

test('live text waits for held catch-up wake batches and flushes pending items before sending delta', async (t) => {
  const n = await startFakeNode(t)
  let releaseCatchup
  let inCatchup = false
  const catchup = new Promise((resolve) => { releaseCatchup = resolve })
  const rt = createRuntime(minimal({
    items: async () => {
      inCatchup = true
      await catchup
      return { items: [{ id: 'reply#0', seq: 1, kind: 'text' }], history: 'h', to_seq: 1 }
    },
  }), { runtime: 'toy', socketPath: n.path })
  t.after(() => { releaseCatchup?.(); rt.stop() })
  rt.start()
  await n.ready
  const watching = n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  await until('driver is catching up', () => inCatchup)
  rt.append('s1', { items: [{ id: 'reply#-1', seq: 2, kind: 'notice', wake: true }], fromSeq: 1, toSeq: 2, history: 'h' })
  rt.stream('s1', 'Resumed progress.')
  await new Promise((r) => setTimeout(r, 40))
  assert.equal(n.all('session.delta').length, 0, 'delta must not overtake held wake items during catch-up')
  releaseCatchup()
  await watching
  await until('resumed progress streams after wake', () => n.last('session.delta')?.text === 'Resumed progress.')
  const events = n.events.filter((entry) => entry.event === 'session.items' || entry.event === 'session.delta')
  assert.deepEqual(events.map((entry) => entry.event), ['session.items', 'session.delta'])
  assert.equal(events[0].params.items.some((item) => item.wake === true), true)

  // A buffered non-wake batch must also flush before a following live delta.
  const before = n.events.length
  rt.append('s1', { items: [{ id: 'step#0', seq: 3, kind: 'thinking', text: 'Working' }], fromSeq: 2, toSeq: 3, history: 'h' })
  rt.stream('s1', 'Next line.')
  await until('next delta arrives after buffered item', () => n.last('session.delta')?.text === 'Next line.')
  const tail = n.events.slice(before).filter((entry) => entry.event === 'session.items' || entry.event === 'session.delta')
  assert.deepEqual(tail.map((entry) => entry.event), ['session.items', 'session.delta'])
  assert.deepEqual(tail[0].params.items.map((item) => item.id), ['step#0'])
})

test('in-place item updates under the same history go out without advancing seq and coalesce by id', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ items: async () => ({ items: [], history: 'h1', to_seq: 5 }) }), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  // Two rapid in-place updates within the append window coalesce into the latest copy of that item id.
  rt.append('s1', { items: [{ id: '5#0', seq: 5, kind: 'thinking', text: 'Step 1' }], fromSeq: 5, toSeq: 5, history: 'h1', update: true })
  rt.append('s1', { items: [{ id: '5#0', seq: 5, kind: 'thinking', text: 'Step 1 + 2' }], fromSeq: 5, toSeq: 5, history: 'h1', update: true })
  await until('coalesced in-place update pushed', () => n.all('session.items').length === 1)
  assert.deepEqual(n.all('session.items')[0], {
    session_id: 's1',
    items: [{ id: '5#0', seq: 5, kind: 'thinking', text: 'Step 1 + 2' }],
    from_seq: 5,
    to_seq: 5,
    history: 'h1',
    update: true,
  })
  // Without update: true, a same-seq append is still ignored as already pushed.
  rt.append('s1', { items: [{ id: '5#0', seq: 5, kind: 'thinking', text: 'ignored' }], fromSeq: 5, toSeq: 5, history: 'h1' })
  await new Promise((r) => setTimeout(r, 120))
  assert.equal(n.all('session.items').length, 1)
  // A follow-up in-place update after flush still goes out under the same history and seq.
  rt.append('s1', { items: [{ id: '5#0', seq: 5, kind: 'thinking', text: 'Step 1 + 2 + 3' }], fromSeq: 5, toSeq: 5, history: 'h1', update: true })
  await until('second in-place update pushed', () => n.all('session.items').length === 2)
  assert.deepEqual(n.all('session.items')[1], {
    session_id: 's1',
    items: [{ id: '5#0', seq: 5, kind: 'thinking', text: 'Step 1 + 2 + 3' }],
    from_seq: 5,
    to_seq: 5,
    history: 'h1',
    update: true,
  })
})

test('mixed append batches preserve update in either order without leaking to later batches', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ cursor: async () => ({ history: 'h', to_seq: 5 }) }), { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1' })
  for (const updateFirst of [true, false]) {
    const from = updateFirst ? 5 : 6
    const append = { items: [{ id: `i${from + 1}`, seq: from + 1 }], fromSeq: from, toSeq: from + 1, history: 'h' }
    const update = { items: [{ id: `i${from}`, seq: from, text: 'revised' }], toSeq: from, history: 'h', update: true }
    for (const a of updateFirst ? [update, append] : [append, update]) rt.append('s1', a)
    await until('mixed batch', () => n.all('session.items').length === (updateFirst ? 1 : 2))
    assert.equal(n.last('session.items').update, true)
    assert.equal(n.last('session.items').items.length, 2)
  }
  rt.append('s1', { items: [], fromSeq: 7, toSeq: 8, history: 'h' })
  await until('plain batch', () => n.all('session.items').length === 3)
  assert.equal(n.last('session.items').update, undefined)
})

test('watch catch-up signals a rewind without claiming its missing items are complete', async (t) => {
  const { page } = await import('../index.js')
  for (const toSeq of [5, 0]) {
    await t.test(`cursor ${toSeq}`, async (t) => {
      const n = await startFakeNode(t)
      const all = Array.from({ length: toSeq }, (_, i) => ({ id: `i${i + 1}`, seq: i + 1, role: 'user', kind: 'text', text: `message ${i + 1}` }))
      const requests = []
      const rt = createRuntime(minimal({ cursor: async () => toSeq, items: async (params) => {
        requests.push(params)
        return page(all, params, { history: 'rewritten', toSeq })
      } }), { runtime: 'toy', socketPath: n.path })
      t.after(() => rt.stop())
      rt.start()
      await n.ready
      await n.peer.call('session.watch', { session_id: 's1', since_seq: 20 })
      assert.equal(n.all('session.items').length, 1)
      const batch = n.last('session.items')
      assert.equal(batch.history, 'rewritten')
      assert.equal(batch.to_seq, toSeq)
      assert.equal(batch.from_seq, 20, 'a continuing batch from another history must trigger reload')
      assert.deepEqual(batch.items, [])
      assert.deepEqual(requests, [{ session_id: 's1', after_seq: 20 }], 'the catch-up response carries the real cursor')
      const reloaded = await n.peer.call('session.items', { session_id: 's1', limit: 80 })
      assert.deepEqual(reloaded.items, all, 'reload recovers every rewound message')
      rt.append('s1', { items: [{ id: 'next', seq: toSeq + 1 }], fromSeq: toSeq, toSeq: toSeq + 1, history: 'rewritten' })
      await until('content after rewind', () => n.all('session.items').length === 2)
      assert.equal(n.last('session.items').to_seq, toSeq + 1)
    })
  }
})

test('an idle watch reads one catch-up page without pushing an empty batch', async (t) => {
  const { page } = await import('../index.js')
  const n = await startFakeNode(t)
  const requests = []
  const rt = createRuntime(minimal({ items: async (params) => {
    requests.push(params)
    return page([{ id: 'last', seq: 20 }], params, { history: 'current', toSeq: 20 })
  } }), { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 20 })
  assert.deepEqual(requests, [{ session_id: 's1', after_seq: 20 }])
  assert.deepEqual(n.all('session.items'), [])
})

test('rewatching a known history reports a same-length rewrite', async (t) => {
  const { page } = await import('../index.js')
  const n = await startFakeNode(t)
  let history = 'old'
  const rt = createRuntime(minimal({ items: async (params) => page([{ id: 'last', seq: 20 }], params, { history, toSeq: 20 }) }), { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  history = 'rewritten'
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 20 })
  assert.equal(n.all('session.items').length, 1)
  assert.equal(n.last('session.items').history, history)
  assert.equal(n.last('session.items').from_seq, 20)
  assert.equal(n.last('session.items').to_seq, 20)
})

test('watch history detects same-length rewrites after unwatch or reconnect', async (t) => {
  const { page } = await import('../index.js')
  for (const reconnect of [false, true]) {
    await t.test(reconnect ? 'reconnect' : 'unwatch', async (t) => {
      const n = await startFakeNode(t)
      let history = 'old'
      let reads = 0
      const rt = createRuntime(minimal({ items: async (params) => {
        reads++
        return page([{ id: 'last', seq: 20 }], params, { history, toSeq: 20 })
      } }), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
      t.after(() => rt.stop())
      rt.start()
      await n.ready
      await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
      if (reconnect) {
        const oldPeer = n.peer
        oldPeer.close()
        await until('runtime reconnects', () => n.peer !== oldPeer && n.hellos === 2)
      } else {
        await n.peer.call('session.unwatch', { session_id: 's1' })
      }
      reads = 0
      await n.peer.call('session.watch', { session_id: 's1', since_seq: 20, history: 'old' })
      assert.equal(reads, 1)
      assert.deepEqual(n.all('session.items'), [], 'known unchanged history stays quiet')
      await n.peer.call('session.unwatch', { session_id: 's1' })
      history = 'rewritten'
      await n.peer.call('session.watch', { session_id: 's1', since_seq: 20, history: 'old' })
      assert.equal(n.all('session.items').length, 1, 'the supplied history survives a lost watch')
      assert.deepEqual(n.last('session.items'), { session_id: 's1', items: [], history, from_seq: 20, to_seq: 20 })
    })
  }
})

test('an explicit zero start resets append watermarks without a new history', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ cursor: async () => ({ history: 'h', to_seq: 20 }) }), { runtime: 'toy', socketPath: n.path })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1' })
  rt.append('s1', { items: Array.from({ length: 5 }, (_, i) => ({ id: `i${i + 1}`, seq: i + 1 })), fromSeq: 0, toSeq: 5, history: 'h' })
  await until('explicit reset', () => n.all('session.items').length === 1)
  assert.equal(n.last('session.items').to_seq, 5)
  rt.append('s1', { items: [{ id: 'next', seq: 6 }], fromSeq: 5, toSeq: 6, history: 'h' })
  await until('content after reset', () => n.all('session.items').length === 2)
  assert.equal(n.last('session.items').to_seq, 6)
})

test('a capped watch catch-up says there is more after its last batch', async (t) => {
  const n = await startFakeNode(t)
  const { page } = await import('../index.js')
  const all = Array.from({ length: 4500 }, (_, i) => ({ id: `${i + 2}#0`, seq: i + 2, role: 'assistant', kind: 'text', text: `item ${i + 2}` }))
  let calls = 0
  const rt = createRuntime(minimal({
    items: async (params) => {
      calls++
      return page(all, params, { history: 'h', toSeq: 4501 })
    },
  }), { runtime: 'toy', socketPath: n.path, retryMs: 20, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 1 })
  const batches = n.all('session.items')
  assert.equal(calls, 8, 'the existing catch-up cap stays at eight pages')
  assert.equal(batches.length, 8)
  assert.equal(batches.at(-1).to_seq, 4001)
  assert.equal(batches.at(-1).more_after, true, 'the client must fetch the 500 items still missing')
  assert.ok(batches.slice(0, -1).every((b) => b.more_after === undefined), 'only the capped batch needs client recovery')
})

test('content committed while a watch catches up is held, then sent once without repeats', async (t) => {
  const n = await startFakeNode(t)
  let release
  const gate = new Promise((r) => { release = r })
  const rt = createRuntime(minimal({
    items: async (p) => {
      await gate
      return { items: [{ id: 'c', seq: 3 }], history: 'h', from_seq: p.after_seq, to_seq: 3 }
    },
  }), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  const watching = n.peer.call('session.watch', { session_id: 's1', since_seq: 2 })
  await until('watching', () => rt.watching('s1'))
  rt.append('s1', { items: [{ id: 'c', seq: 3 }], fromSeq: 2, toSeq: 3, history: 'h' }) // also in the catch-up page
  rt.append('s1', { items: [{ id: 'd', seq: 4 }], fromSeq: 3, toSeq: 4, history: 'h' })
  release()
  await watching
  await until('two batches', () => n.all('session.items').length === 2)
  const [catchUp, live] = n.all('session.items')
  assert.deepEqual([catchUp.from_seq, catchUp.to_seq, live.from_seq, live.to_seq], [2, 3, 3, 4])
  assert.deepEqual(live.items.map((i) => i.id), ['d'])
})

test('asking the phone marks the session as needing you, and a withdrawn ask takes the card off', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ hasLocalClient: () => false }), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(rt.phoneFirst('s1'), true, 'no local UI open: the phone first')
  assert.equal(rt.phoneFirst('unlisted'), false)
  let answer
  n.resolve = () => new Promise((r) => { answer = r })
  const id = interactionId('toy', 's1', 'permission', 'call-1')
  const asking = rt.ask({ id, kind: 'permission', session_id: 's1', tool_name: 'Bash' })
  await until('blocked', () => n.last('runtime.sessions')?.sessions[0].status === 'blocked')
  answer({ allow: true })
  assert.deepEqual(await asking, { allow: true })
  await until('back to idle', () => n.last('runtime.sessions')?.sessions[0].status === 'idle')

  n.resolve = () => new Promise(() => {}) // never answered
  const ac = new AbortController()
  const withdrawn = rt.ask({ id: 'toy-2', kind: 'question', session_id: 's1' }, { signal: ac.signal })
  await until('asked', () => n.asked.length === 2)
  ac.abort(new Error('gone'))
  await assert.rejects(withdrawn, /gone/)
  await until('cancelled', () => n.last('interaction.cancel')?.id === 'toy-2')
  assert.equal(interactionId('toy', 's1', 'permission', 'call-1'), id, 'stable ids')
})

for (const loss of ['hub', 'node']) {
  test(`an aborted ask withdraws its card after ${loss} recovery`, async (t) => {
    const n = await startFakeNode(t)
    const rt = createRuntime(minimal(), { runtime: `toy-cancel-${loss}`, socketPath: n.path, retryMs: loss === 'node' ? 200 : 20, pushDelayMs: 5, resumeWaitMs: 5000 })
    t.after(() => rt.stop())
    rt.start()
    await n.ready
    await until('listed', () => n.last('runtime.sessions'))
    n.resolve = () => new Promise(() => {})
    const ac = new AbortController()
    const asking = rt.ask({ id: `cancel-after-${loss}`, kind: 'question', session_id: 's1' }, { signal: ac.signal })
    await until('asked', () => n.asked.length === 1)
    if (loss === 'hub') {
      n.setHub(false)
      await until('hub lost', () => !rt.hubConnected)
    } else {
      n.peer.close(new Error('node restarted'))
      await until('node lost', () => !rt.connected)
    }
    ac.abort(new Error('answered elsewhere'))
    await assert.rejects(asking, /answered elsewhere/)
    assert.equal(n.all('interaction.cancel').length, 0, 'the cancel waits for a connected hub')
    if (loss === 'hub') n.setHub(true)
    await until('cancel delivered after recovery', () => n.last('interaction.cancel')?.id === `cancel-after-${loss}`)
    assert.equal(n.asked.length, 1, 'the aborted ask is not resumed')
  })
}

test('an aborted ask retains its cancel when the peer has just closed', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy-cancel-closing', socketPath: n.path, retryMs: 200, pushDelayMs: 5, resumeWaitMs: 5000 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  n.resolve = () => new Promise(() => {})
  const ac = new AbortController()
  const asking = rt.ask({ id: 'cancel-while-closing', kind: 'question', session_id: 's1' }, { signal: ac.signal })
  await until('asked', () => n.asked.length === 1)
  const close = Peer.prototype.close
  let closing = true
  Peer.prototype.close = function (err) {
    close.call(this, err)
    if (closing && this.prefix === 'd') ac.abort(new Error('answered elsewhere'))
  }
  t.after(() => { Peer.prototype.close = close })
  n.peer.close(new Error('node restarted'))
  await assert.rejects(asking, /connection closed/)
  closing = false
  assert.equal(n.all('interaction.cancel').length, 0)
  await until('cancel delivered after recovery', () => n.last('interaction.cancel')?.id === 'cancel-while-closing')
  assert.equal(n.asked.length, 1, 'the aborted ask is not resumed')
})

test("a card up longer than the resume wait still waits for the hub that went away under it, from when it went", async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5, resumeWaitMs: 400 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  const gone = () => Promise.reject(new RpcError('node going away', 'node_gone')) // the node's hub link dropped under the card
  let drop
  n.resolve = () => new Promise((_, reject) => { drop = () => reject(new RpcError('node going away', 'node_gone')) })
  const id = interactionId('toy', 's1', 'permission', 'call-1')
  const asking = rt.ask({ id, kind: 'permission', session_id: 's1', tool_name: 'Bash' })
  await until('on the phone', () => n.asked.length === 1 && drop)
  await new Promise((r) => setTimeout(r, 600)) // up longer than the wait
  n.resolve = gone
  n.setHub(false)
  drop()
  await new Promise((r) => setTimeout(r, 250)) // a while with no hub
  n.resolve = () => ({ allow: true })
  n.setHub(true) // back: the next ask, with the same id, is answered
  assert.deepEqual(await asking, { allow: true }, 'the next hub answers it, not a dismiss at once')
  assert.deepEqual(n.asked.map((ix) => ix.id), [id, id], 'asked again with the same id')

  // A hub that does not come back: the wait ends that long after it went, not at once and not never.
  n.resolve = () => new Promise((_, reject) => { drop = () => reject(new RpcError('node going away', 'node_gone')) })
  drop = undefined
  const again = rt.ask({ id: 'toy-2', kind: 'permission', session_id: 's1', tool_name: 'Bash' })
  await until('on the phone again', () => drop)
  await new Promise((r) => setTimeout(r, 600))
  n.resolve = gone
  n.setHub(false)
  const lostAt = Date.now()
  drop()
  assert.deepEqual(await again, { dismiss: true })
  const waited = Date.now() - lostAt
  assert.ok(waited >= 350 && waited < 1500, `dismissed ${waited} ms after the hub went`)
})

test('rewrites held while a watch catches up go out in order: to another history and back', async (t) => {
  const n = await startFakeNode(t)
  let release
  const gate = new Promise((r) => { release = r })
  const rt = createRuntime(minimal({
    items: async (p) => {
      await gate
      return { items: [{ id: 'c', seq: 10 }], history: 'h1', from_seq: p.after_seq, to_seq: 10 }
    },
  }), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  const watching = n.peer.call('session.watch', { session_id: 's1', since_seq: 9 })
  await until('watching', () => rt.watching('s1'))
  rt.append('s1', { items: [], toSeq: 6, history: 'h2' }) // to a shorter branch
  rt.append('s1', { items: [], toSeq: 10, history: 'h1' }) // and back to the first, as long as it was
  release()
  await watching
  await until('the catch-up and both moves', () => n.all('session.items').length === 3)
  assert.deepEqual(n.all('session.items').map((b) => [b.history, b.to_seq]), [['h1', 10], ['h2', 6], ['h1', 10]])
})

test('live text is throttled to the latest whole text, and ends empty', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  rt.stream('s1', 'ignored: not watched')
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  for (const text of ['H', 'He', 'Hel', 'Hell', 'Hello']) rt.stream('s1', text)
  await until('the latest text', () => n.last('session.delta')?.text === 'Hello', 1000)
  assert.ok(n.all('session.delta').length <= 2, 'updates within one window are merged')
  assert.ok(!n.all('session.delta').some((d) => d.text.startsWith('ignored')), 'unwatched sessions get no deltas')
  rt.streamEnd('s1')
  await until('end', () => n.last('session.delta')?.text === '')
})

test('a stream ending after unwatch clears the published text and cancels its pending batch', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  rt.stream('s1', 'Previous reply.')
  await until('the published reply', () => n.last('session.delta')?.text === 'Previous reply.')
  rt.stream('s1', 'Previous reply. More text.')
  await n.peer.call('session.unwatch', { session_id: 's1' })
  rt.streamEnd('s1')
  await until('the unwatched reply ends', () => n.last('session.delta')?.text === '', 2000)
  await new Promise((resolve) => setTimeout(resolve, 150))
  assert.equal(n.last('session.delta').text, '', 'the pending batch cannot restore the old text')
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  rt.stream('s1', 'Previous reply.')
  await until('a new reply may repeat the earlier words', () => n.last('session.delta')?.text === 'Previous reply.')
  rt.streamEnd('never-watched')
  await new Promise((resolve) => setTimeout(resolve, 20))
  assert.ok(n.all('session.delta').every((delta) => delta.session_id === 's1'))
})

test('live text flows again after stop() then start(): a text pending at stop does not hold the session silent', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  rt.stream('s1', 'a')
  await until('the first text', () => n.last('session.delta')?.text === 'a')
  rt.stream('s1', 'ab') // within the window: waits on the throttle timer
  rt.stop()
  rt.start()
  await until('hello again', () => n.hellos === 2)
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  rt.stream('s1', 'abc')
  await until('the text after the restart', () => n.last('session.delta')?.text === 'abc', 1000)
})

test('item helpers cut long text the way the apps expect', () => {
  const it = light({ kind: 'text', text: 'a'.repeat(PAGE_TEXT + 10) })
  assert.equal(it.full_size, PAGE_TEXT + 10)
  assert.match(it.text, /已截断/)
})

test('the echo example is a complete runtime: start, send, stream, ask, page', async (t) => {
  const n = await startFakeNode(t)
  const { runtime } = createEchoRuntime({ socketPath: n.path, retryMs: 20, pushDelayMs: 5, answerFirst: 'phone' })
  t.after(() => runtime.stop())
  runtime.start()
  await n.ready
  assert.equal(n.hello.launcher, 'echo')
  const { session } = await n.peer.call('session.start', { cwd: '/w/demo' })
  await n.peer.call('session.watch', { session_id: session.session_id, since_seq: 0 })
  n.resolve = () => ({ allow: true })
  await n.peer.call('session.send', { session_id: session.session_id, text: 'please ask first', client_msg_id: 'm1' })
  await until('reply', () => n.all('session.items').flatMap((b) => b.items).some((i) => i.text === 'echo: please ask first'), 3000)
  assert.equal(n.asked[0].kind, 'permission')
  assert.ok(n.all('session.delta').some((d) => d.text.startsWith('echo')))
  const page = await n.peer.call('session.items', { session_id: session.session_id, limit: 10 })
  assert.deepEqual(page.items.map((i) => i.role), ['user', 'assistant'])
  const batches = n.all('session.items')
  assert.deepEqual(batches.slice(1).filter((b, i) => b.from_seq !== batches[i].to_seq), [])
})

test('explicit phone-first admits an unlisted session, while local-first and a missing peer do not (#8203)', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5, answerFirst: 'phone' })
  t.after(() => rt.stop())
  assert.equal(rt.phoneFirst('s1'), false, 'no node peer: nobody can answer on the phone')
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(rt.phoneFirst('unlisted'), true, 'explicit phone-first does not depend on inventory membership')
  assert.equal(rt.phoneFirst('s1'), true, 'the listed behavior is unchanged')
  n.setHub(false)
  await until('hub gone', () => !rt.hubConnected)
  assert.equal(rt.phoneFirst('unlisted'), false, 'no hub: even an explicitly phone-first session asks locally')

  const other = await startFakeNode(t)
  const local = createRuntime(minimal(), { runtime: 'toy-local', socketPath: other.path, retryMs: 20, pushDelayMs: 5, answerFirst: 'local' })
  t.after(() => local.stop())
  assert.equal(local.phoneFirst('s1'), false, 'local-first also stays local without a peer')
  local.start()
  await other.ready
  await until('listed', () => other.last('runtime.sessions'))
  assert.equal(local.phoneFirst('s1'), false)
  assert.equal(local.phoneFirst('unlisted'), false)
  other.peer.notify('runtime.driven', { session_id: 'unlisted' })
  await until('phone-driven', () => local.touched('unlisted'))
  assert.equal(local.phoneFirst('unlisted'), false, 'recent phone activity never overrides explicit local-first')
})

test('recent phone-driven automatic sessions are phone-first before listing, unlike unrelated unlisted sessions (#8203)', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ hasLocalClient: () => false }), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(rt.phoneFirst('s1'), true, 'a listed session with no local client remains phone-first')
  assert.equal(rt.phoneFirst('unrelated'), false, 'no local client alone does not admit an unknown automatic session')
  n.peer.notify('runtime.driven', { session_id: 'session-new' })
  await until('phone-driven', () => rt.touched('session-new'))
  assert.equal(rt.phoneFirst('session-new'), true, 'recent phone activity is enough before the delayed inventory')
  assert.equal(rt.phoneFirst('unrelated'), false, 'admitting one phone-driven session does not admit every unlisted session')
})

test('without a hub on the node the phone is not asked first; it is again once the hub is back', async (t) => {
  const n = await startFakeNode(t, { hub: false })
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5, answerFirst: 'phone' })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(rt.hubConnected, false)
  assert.equal(rt.phoneFirst('s1'), false, 'no hub: the local UI asks, even with answerFirst phone')
  n.setHub(true)
  await until('hub back', () => rt.hubConnected)
  assert.equal(rt.phoneFirst('s1'), true)
  n.setHub(false)
  await until('hub gone', () => !rt.hubConnected)
  assert.equal(rt.phoneFirst('s1'), false)
})

test('while the socket file is there (a node restarting) it retries every second, not on the long back-off', async (t) => {
  const { mkdtempSync, rmSync, writeFileSync } = await import('node:fs')
  const { join } = await import('node:path')
  const dir = mkdtempSync('/tmp/jrt-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const path = join(dir, 'runtime.sock')
  writeFileSync(path, '') // what a node that went away leaves: the file, nobody listening
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: path, retryMs: 8000 }) // a long back-off otherwise
  t.after(() => rt.stop())
  rt.start()
  await new Promise((r) => setTimeout(r, 300))
  rmSync(path)
  const started = Date.now()
  const n = await startFakeNode(t, { path }) // the node is back
  await n.ready
  assert.ok(Date.now() - started < 2000, `reconnected after ${Date.now() - started} ms`)
})

test('page: from the end by turns or limit, back from a seq, forward with a cut, and a span', async () => {
  const { page } = await import('../index.js')
  const it = (seq, role, kind = 'text', text = `${role}${seq}`) => ({ id: `${seq}#0`, seq, role, kind, text })
  const all = [it(1, 'user'), it(2, 'assistant'), it(3, 'user'), it(4, 'assistant'), { ...it(4, 'assistant', 'tool_use'), id: '4#1' }, it(5, 'user'), it(6, 'assistant')]
  const at = { history: 'h', toSeq: 9 }
  const last = page(all, { turns: 2 }, at)
  assert.deepEqual(last.items.map((i) => i.seq), [3, 4, 4, 5, 6])
  assert.equal(last.has_more, true)
  assert.equal(last.to_seq, 9)
  const back = page(all, { before_seq: 5, limit: 1 }, at)
  assert.deepEqual(back.items.map((i) => i.id), ['4#0', '4#1'], 'a record is not split')
  assert.equal(back.to_seq, 4)
  const fwd = page(all, { after_seq: 4 }, at)
  assert.deepEqual([fwd.from_seq, fwd.to_seq, fwd.more_after, fwd.items.length], [4, 9, false, 2])
  const span = page(all, { from_seq: 2, through_seq: 3 }, at)
  assert.deepEqual(span.items.map((i) => i.seq), [2, 3])
  const many = Array.from({ length: 600 }, (_, i) => it(i + 1, 'assistant'))
  const cut = page(many, { after_seq: 0 }, { history: 'h', toSeq: 600 })
  assert.deepEqual([cut.items.length, cut.more_after, cut.to_seq], [500, true, 500])
})

test('a runtime that requires a node feature stays off an older node until a new one replaces the socket', async (t) => {
  const { mkdtempSync, rmSync } = await import('node:fs')
  const { join } = await import('node:path')
  const dir = mkdtempSync('/tmp/jrt-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const path = join(dir, 'runtime.sock')
  const old = await startFakeNode(t, { path, features: [] })
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: path, retryMs: 20, pushDelayMs: 5, requires: ['prefer_running'] })
  t.after(() => rt.stop())
  rt.start()
  await old.ready
  await new Promise((r) => setTimeout(r, 1500))
  assert.equal(rt.connected, false)
  assert.equal(old.all('runtime.sessions').length, 0, 'nothing listed to an older node')
  assert.equal(old.hellos, 1, 'not reconnecting to the older node meanwhile')
  old.close() // the node restarts as a newer one: a new socket file at the same path
  rmSync(path, { force: true })
  const fresh = await startFakeNode(t, { path })
  await fresh.ready
  await until('listed to the new node', () => fresh.last('runtime.sessions'), 10_000)
  assert.equal(rt.connected, true)
})

test('a send is made once per client_msg_id: a retry gets the first answer, a failed one may go again', async (t) => {
  const n = await startFakeNode(t)
  let calls = 0
  let fail = false
  const rt = createRuntime({ ...minimal(), send: async () => { calls++; if (fail) throw new Error('down') } }, { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await Promise.all([n.peer.call('session.send', { session_id: 's1', text: 'a', client_msg_id: 'm1' }), n.peer.call('session.send', { session_id: 's1', text: 'a', client_msg_id: 'm1' })])
  await n.peer.call('session.send', { session_id: 's1', text: 'a', client_msg_id: 'm1' })
  assert.equal(calls, 1)
  fail = true
  await assert.rejects(n.peer.call('session.send', { session_id: 's1', text: 'b', client_msg_id: 'm2' }))
  fail = false
  await n.peer.call('session.send', { session_id: 's1', text: 'b', client_msg_id: 'm2' })
  assert.equal(calls, 3)
  assert.equal(rt.touched('s1'), true)
  assert.equal(rt.touched('s2'), false)
})

test('an in-doubt send stays cached, while an explicit non-delivery may go again', async (t) => {
  const n = await startFakeNode(t)
  const calls = new Map()
  const rt = createRuntime({ ...minimal(), send: async ({ client_msg_id: id }) => {
    calls.set(id, (calls.get(id) ?? 0) + 1)
    throw new RpcError(`send ${id}`, 'send_failed', { delivered: id })
  } }, { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  const send = async (id) => {
    const err = await n.peer.call('session.send', { session_id: 's1', text: 'x', client_msg_id: id }).then(() => undefined, (err) => err)
    assert.ok(err, 'the send must fail')
    assert.equal(err.code, 'send_failed')
    assert.equal(err.message, `send ${id}`)
    assert.deepEqual(err.data, { delivered: id })
    return err
  }
  const first = await send('unknown')
  const retry = await send('unknown')
  assert.deepEqual({ message: retry.message, code: retry.code, data: retry.data },
    { message: first.message, code: first.code, data: first.data })
  assert.equal(calls.get('unknown'), 1, 'an in-doubt retry returns the original outcome without calling the driver')
  await send('none')
  await send('none')
  assert.equal(calls.get('none'), 2, 'an explicit non-delivery may be retried')
})

test('an in-doubt send stays cached across a node-only restart', async (t) => {
  const { mkdtempSync, rmSync } = await import('node:fs')
  const { join } = await import('node:path')
  const dir = mkdtempSync('/tmp/jrt-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const path = join(dir, 'runtime.sock')
  const first = await startFakeNode(t, { path })
  let calls = 0
  const rt = createRuntime({ ...minimal(), send: async () => {
    calls++
    throw new RpcError('send outcome is unknown', 'send_failed', { delivered: 'unknown' })
  } }, { runtime: 'toy', socketPath: path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  await rt.start()
  await first.ready
  const params = { session_id: 's1', text: 'x', client_msg_id: 'm-unknown' }
  const send = (node) => assert.rejects(node.peer.call('session.send', params),
    (err) => err.code === 'send_failed' && err.data?.delivered === 'unknown')
  await send(first)
  first.peer.close(new Error('node restarts'))
  await until('node disconnected', () => !rt.connected)
  first.close()
  rmSync(path, { force: true })
  const second = await startFakeNode(t, { path })
  await second.ready
  await send(second)
  assert.equal(calls, 1, 'the live companion answers the retry from its cache')
})

test('session.send_unseen supersedes only a completed in-doubt send', async (t) => {
  const n = await startFakeNode(t)
  let calls = 0
  const rt = createRuntime({ ...minimal(), send: async () => {
    calls++
    if (calls === 1) throw new RpcError('send outcome is unknown', 'send_failed', { delivered: 'unknown' })
  } }, { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  await rt.start()
  await n.ready
  const params = { session_id: 's1', text: 'x', client_msg_id: 'm-superseded' }
  await assert.rejects(n.peer.call('session.send', params), (err) => err.data?.delivered === 'unknown')
  await assert.rejects(n.peer.call('session.send', params), (err) => err.data?.delivered === 'unknown')
  assert.equal(calls, 1)
  assert.deepEqual(await n.peer.call('session.send_unseen', params), { ok: true })
  assert.equal(calls, 2, 'the checked owner-forced retry calls the driver')
  await n.peer.call('session.send', params)
  await n.peer.call('session.send_unseen', params)
  assert.equal(calls, 2, 'neither retry supersedes the acknowledged attempt')
})

test('session.send_unseen does not supersede a pending send', async (t) => {
  const n = await startFakeNode(t)
  let calls = 0
  let release
  const gate = new Promise((resolve) => { release = resolve })
  const rt = createRuntime({ ...minimal(), send: async () => {
    calls++
    await gate
    throw new RpcError('send outcome is unknown', 'send_failed', { delivered: 'unknown' })
  } }, { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  await rt.start()
  await n.ready
  const params = { session_id: 's1', text: 'x', client_msg_id: 'm-pending' }
  const first = n.peer.call('session.send', params)
  await until('the original driver call', () => calls === 1)
  const superseding = n.peer.call('session.send_unseen', params)
  await new Promise((resolve) => setTimeout(resolve, 20))
  release()
  await Promise.all([
    assert.rejects(first, (err) => err.data?.delivered === 'unknown'),
    assert.rejects(superseding, (err) => err.data?.delivered === 'unknown'),
  ])
  assert.equal(calls, 1, 'the checked retry waits for the pending attempt instead of replacing it')
})

test('an in-doubt send expires after ten minutes', async (t) => {
  t.mock.timers.enable({ apis: ['Date'] })
  const n = await startFakeNode(t)
  let calls = 0
  const rt = createRuntime({ ...minimal(), send: async () => {
    calls++
    if (calls === 1) throw new RpcError('send outcome is unknown', 'send_failed', { delivered: 'unknown' })
  } }, { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  await rt.start()
  await n.ready
  const params = { session_id: 's1', text: 'x', client_msg_id: 'm-expiry' }
  await assert.rejects(n.peer.call('session.send', params), (err) => err.data?.delivered === 'unknown')
  t.mock.timers.tick(10 * 60 * 1000)
  await assert.rejects(n.peer.call('session.send', params), (err) => err.data?.delivered === 'unknown')
  assert.equal(calls, 1, 'the outcome remains cached through the TTL boundary')
  t.mock.timers.tick(1)
  assert.deepEqual(await n.peer.call('session.send', params), { ok: true })
  assert.equal(calls, 2, 'an expired in-doubt outcome no longer blocks a resend')
})

test('recent: pinned and phone-touched sessions always, the rest by age up to the limit, newest first', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ send: async () => {} }), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.send', { session_id: 'old-touched', text: 'x', client_msg_id: 'r1' })
  const day = 86400_000
  const row = (id, ago, running = false) => ({ session_id: id, updated_at: new Date(Date.now() - ago).toISOString(), running })
  const rows = [row('a', 1000), row('b', 2000), row('c', 3000), row('ancient', 30 * day), row('old-running', 40 * day, true), row('old-touched', 50 * day)]
  const got = rt.recent(rows, { days: 7, max: 4, pinned: (r) => r.running })
  assert.deepEqual(got.map((r) => r.session_id), ['a', 'b', 'old-running', 'old-touched'])
  const { appToolName } = await import('../index.js')
  assert.deepEqual([appToolName('bash'), appToolName('find', { find: 'Glob' }), appToolName('mystery')], ['Bash', 'Glob', 'mystery'])
})

test('hello carries one instance per runtime, the same on every reconnect', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  const first = n.hello.instance
  assert.match(first, /^[0-9a-f-]{36}$/)
  n.peer.close(new Error('node restarts'))
  await until('hello again', () => n.hellos >= 2)
  assert.equal(n.hello.instance, first)
  const other = createRuntime(minimal(), { runtime: 'toy2', socketPath: n.path, retryMs: 20 })
  t.after(() => other.stop())
  other.start()
  await until('the other runtime', () => n.hello.runtime === 'toy2')
  assert.notEqual(n.hello.instance, first)
})

test('a runtime id another process holds is not fought over: it is tried again only every 30 seconds', async (t) => {
  const n = await startFakeNode(t)
  n.conflict()
  const logs = []
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, log: (m) => logs.push(m) })
  t.after(() => rt.stop())
  rt.start()
  await until('refused', () => n.hellos >= 1)
  await new Promise((r) => setTimeout(r, 300))
  assert.equal(n.hellos, 1, 'not retried on the short back-off')
  assert.equal(rt.connected, false)
  assert.ok(logs.some((m) => m.includes('another runtime id')), logs.join('\n'))
})

test('the hub\'s tools: listed and called through the node, onHub after hello and as the hub comes and goes', async (t) => {
  const n = await startFakeNode(t, { tools: [{ name: 'phone_contacts', description: 'Contacts', input_schema: { type: 'object' } }] })
  const hubs = []
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, onHub: (c) => hubs.push(c) })
  t.after(() => rt.stop())
  assert.deepEqual(await rt.tools(), [], 'no node yet: none')
  rt.start()
  await n.ready
  await until('onHub after hello', () => hubs.length === 1)
  assert.deepEqual(hubs, [true])
  assert.deepEqual((await rt.tools()).map((x) => x.name), ['phone_contacts'])
  const res = await rt.callTool({ name: 'phone_contacts', args: { q: 'li' }, session_id: 's1' })
  assert.equal(res.content[0].text, 'phone_contacts ran')
  assert.deepEqual(n.toolCalls[0], { name: 'phone_contacts', args: { q: 'li' }, session_id: 's1' })
  n.setHub(false)
  await until('onHub: gone', () => hubs.length === 2)
  n.setHub(false) // no change: not called again
  n.setHub(true)
  await until('onHub: back', () => hubs.length === 3)
  assert.deepEqual(hubs, [true, false, true])
})

test('a refusal with data reaches the node with its data (delivered: none)', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({ send: async () => { throw new RpcError('handed over', 'handover', { delivered: 'none' }) } }), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await assert.rejects(n.peer.call('session.send', { session_id: 's1', text: 'x' }), (err) => err.code === 'handover' && err.data?.delivered === 'none')
})

test('a runtime running several agents: launchers in hello and when they change, modes, takeovers', async (t) => {
  const n = await startFakeNode(t)
  const got = []
  const launchers = [{ name: 'paseo-claude', agent: 'claude', model: 'Opus', models: [{ id: 'opus', name: 'Opus', default: true }] }]
  const rt = createRuntime(minimal({
    start: async (p) => {
      got.push(['start', p])
      // What it asked for and does not run with goes back with the row (caps.same_kind).
      return { session: { session_id: p.resume ?? 'new', status: 'idle' }, ...(p.resume ? {} : { instead: { launcher: p.launcher } }) }
    },
    modes: async (p) => got.push(['modes', p]),
  }), { runtime: 'paseo', socketPath: n.path, retryMs: 20, launchers, resume: true, readonly: true, sameKind: true })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  assert.equal(n.hello.caps.modes, true)
  assert.equal(n.hello.caps.resume, true)
  assert.equal(n.hello.caps.readonly, true)
  assert.equal(n.hello.caps.same_kind, true)
  assert.deepEqual(n.hello.launchers, launchers)
  await n.peer.call('session.modes', { session_id: 's1', permission_mode: 'plan', effort: 'high' })
  const res = await n.peer.call('session.start', { cwd: '/w', resume: 'cccccccc-0000-0000-0000-000000000001', launcher: 'paseo-claude', model: 'opus' })
  assert.equal(res.session.session_id, 'cccccccc-0000-0000-0000-000000000001')
  const fresh = await n.peer.call('session.start', { cwd: '/w', launcher: 'cc-gateway', agent_type: 'claude' })
  assert.deepEqual(fresh.instead, { launcher: 'cc-gateway' })
  assert.equal(got.at(-1)[1].agent_type, 'claude')
  assert.deepEqual(got[0], ['modes', { session_id: 's1', permission_mode: 'plan', effort: 'high' }])
  assert.equal(got[1][1].model, 'opus')
  rt.launchersChanged([{ name: 'paseo-codex', agent: 'paseo' }])
  await until('runtime.launchers', () => n.last('runtime.launchers'))
  assert.deepEqual(n.last('runtime.launchers').launchers, [{ name: 'paseo-codex', agent: 'paseo' }])
})

test('session.modes forwards optional settled permission_mode returned by the driver (#17239)', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal({
    modes: async (p) => (p.permission_mode === 'auto' ? { permission_mode: 'bypassPermissions' } : undefined),
  }), { runtime: 'agy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  assert.deepEqual(await n.peer.call('session.modes', { session_id: 's1', permission_mode: 'auto' }), { ok: true, permission_mode: 'bypassPermissions' })
  assert.deepEqual(await n.peer.call('session.modes', { session_id: 's1', permission_mode: 'plan' }), { ok: true })
})

test('archive: caps.archive only with both ways, calls reach the driver, one way alone is not offered', async (t) => {
  const n = await startFakeNode(t)
  const got = []
  const rt = createRuntime(minimal({
    archive: async (p) => got.push(['archive', p.session_id]),
    unarchive: async (p) => got.push(['unarchive', p.session_id]),
  }), { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  assert.equal(n.hello.caps.archive, true)
  assert.deepEqual(await n.peer.call('session.archive', { session_id: 's1' }), { ok: true })
  assert.deepEqual(await n.peer.call('session.unarchive', { session_id: 's1' }), { ok: true })
  assert.deepEqual(got, [['archive', 's1'], ['unarchive', 's1']])

  const half = await startFakeNode(t)
  const rt2 = createRuntime(minimal({ archive: async () => {} }), { runtime: 'half', socketPath: half.path, retryMs: 20 })
  t.after(() => rt2.stop())
  rt2.start()
  await half.ready
  assert.equal(half.hello.caps.archive, undefined)
  await assert.rejects(half.peer.call('session.archive', { session_id: 's1' }), (err) => err.code === 'unsupported')
})

test('workspaces: caps.workspaces with createWorkspace and prepareWorkspace, the list on change and to every node, creation and its folder', async (t) => {
  const n = await startFakeNode(t)
  const got = []
  const row = { workspace_id: 'ws-1', title: 'main', cwd: '/w/app', kind: 'checkout', branch: 'main', space_id: 'p1', space_name: 'app' }
  const rt = createRuntime(minimal({
    createWorkspace: async (p) => {
      got.push(['create', p])
      return { workspace: { workspace_id: 'ws-2', title: p.branch, cwd: `/w/trees/${p.name}`, kind: 'worktree', branch: p.branch, owned: true } }
    },
    prepareWorkspace: async (p) => {
      got.push(['prepare', p])
      return { checkouts: '/w/trees' }
    },
  }), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.workspacesChanged([row]) // before any node: said once one is there
  rt.start()
  await n.ready
  assert.equal(n.hello.caps.workspaces, true)
  await until('the workspaces after hello', () => n.last('runtime.workspaces'))
  assert.deepEqual(n.last('runtime.workspaces'), { workspaces: [row] })
  assert.deepEqual(await n.peer.call('workspace.prepare', { cwd: '/w/app', repo_root: '/w/app' }), { checkouts: '/w/trees' })
  const made = await n.peer.call('workspace.create', { cwd: '/w/app', workspace_id: 'ws-1', branch: 'feat', create_branch: true, base: 'main', name: 'feat', key: 'k1' })
  assert.equal(made.workspace.cwd, '/w/trees/feat')
  assert.deepEqual(got, [['prepare', { cwd: '/w/app', repo_root: '/w/app' }],
    ['create', { cwd: '/w/app', workspace_id: 'ws-1', branch: 'feat', create_branch: true, base: 'main', name: 'feat', key: 'k1' }]])
  rt.workspacesChanged([row, made.workspace])
  await until('the new list', () => n.last('runtime.workspaces')?.workspaces.length === 2)
  // Renaming and archiving are asked of the driver that has them, and refused as unsupported by one that has not.
  await assert.rejects(n.peer.call('workspace.rename', { workspace_id: 'ws-2', title: 'x' }), (err) => err.code === 'unsupported')
  await assert.rejects(n.peer.call('workspace.archive', { workspace_id: 'ws-2' }), (err) => err.code === 'unsupported')
  const n2 = await startFakeNode(t)
  const rt3 = createRuntime(minimal({
    createWorkspace: async () => ({}),
    prepareWorkspace: async () => ({ checkouts: '/w/trees' }),
    renameWorkspace: async (p) => { got.push(['rename', p]); return p.space_id ? { space_name: p.title } : { workspace: { ...row, title: p.title } } },
    archiveWorkspace: async (p) => { got.push(['archive', p]); return { archived_at: '2026-11-12T00:00:00.000Z' } },
  }), { runtime: 'toy2', socketPath: n2.path, retryMs: 20 })
  t.after(() => rt3.stop())
  rt3.start()
  await n2.ready
  assert.equal((await n2.peer.call('workspace.rename', { workspace_id: 'ws-1', title: 'Main' })).workspace.title, 'Main')
  assert.deepEqual(await n2.peer.call('workspace.rename', { space_id: 'p1', title: 'App' }), { space_name: 'App' })
  assert.deepEqual(await n2.peer.call('workspace.archive', { workspace_id: 'ws-1' }), { archived_at: '2026-11-12T00:00:00.000Z' })
  assert.deepEqual(got.slice(2), [['rename', { workspace_id: 'ws-1', title: 'Main' }], ['rename', { space_id: 'p1', title: 'App' }],
    ['archive', { workspace_id: 'ws-1' }]])

  // Without both nothing is declared, said or answered: the node asks where the worktrees go before each one it has
  // made, so either alone could make none.
  const renames = { renameWorkspace: async () => ({ space_name: 'x' }), archiveWorkspace: async () => ({}) }
  for (const [name, half] of [['prepare-only', { prepareWorkspace: async () => ({ checkouts: '/x' }), ...renames }],
    ['create-only', { createWorkspace: async () => ({ workspace: { workspace_id: 'ws-9', cwd: '/x/y' } }), ...renames }]]) {
    const plain = await startFakeNode(t)
    const rt2 = createRuntime(minimal(half), { runtime: name, socketPath: plain.path, retryMs: 20 })
    t.after(() => rt2.stop())
    rt2.workspacesChanged([row])
    rt2.start()
    await plain.ready
    assert.equal(plain.hello.caps.workspaces, undefined, name)
    await assert.rejects(plain.peer.call('workspace.create', { cwd: '/w/app' }), (err) => err.code === 'unsupported')
    await assert.rejects(plain.peer.call('workspace.prepare', { cwd: '/w/app' }), (err) => err.code === 'unsupported')
    await assert.rejects(plain.peer.call('workspace.rename', { space_id: 'p1', title: 'x' }), (err) => err.code === 'unsupported')
    await assert.rejects(plain.peer.call('workspace.archive', { workspace_id: 'ws-1' }), (err) => err.code === 'unsupported')
    assert.equal(plain.last('runtime.workspaces'), undefined, name)
  }
})

test('a watch for a session the node reads from its transcript streams live text only', async (t) => {
  const n = await startFakeNode(t)
  let paged = 0
  const rt = createRuntime(minimal({ items: async () => { paged++; return { items: [], to_seq: 5 } } }), { runtime: 'paseo', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0, transcript: true })
  assert.equal(paged, 0, 'no catch-up: the items are the transcript\'s')
  assert.ok(rt.watching('s1'))
  rt.append('s1', { items: [{ id: 'x', seq: 6 }], fromSeq: 5, toSeq: 6, history: 'h' })
  rt.stream('s1', 'thinking about')
  await until('the live text', () => n.all('session.delta').length === 1)
  await new Promise((r) => setTimeout(r, 150))
  assert.equal(n.all('session.items').length, 0)
  assert.equal(n.all('session.delta')[0].text, 'thinking about')
})

test('the driver hears which sessions are watched: started, stopped, and all of them when the node is new', async (t) => {
  const n = await startFakeNode(t)
  const heard = []
  const rt = createRuntime(minimal({ cursor: async () => 0, watchChanged: (id, on, o) => heard.push([id, on, o?.transcript]) }),
    { runtime: 'toy', socketPath: n.path, retryMs: 20 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 's1', since_seq: 0 })
  await n.peer.call('session.watch', { session_id: 's2', since_seq: 0, transcript: true })
  await n.peer.call('session.unwatch', { session_id: 's1' })
  await n.peer.call('session.unwatch', { session_id: 'never' })
  assert.deepEqual(heard, [['s1', true, false], ['s2', true, true], ['s1', false, undefined]])
  n.peer.close(new Error('node restarting'))
  await until('a new hello', () => n.hellos === 2)
  assert.deepEqual(heard.at(-1), ['s2', false, undefined], 'a new node asks again for what it watches')
})

test('a card for a session not listed yet: the list goes out first, so the node takes the card', async (t) => {
  const n = await startFakeNode(t)
  let rows = []
  const rt = createRuntime(minimal({ listSessions: async () => rows }), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5000 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('the first list', () => n.last('runtime.sessions'))
  const order = []
  const orig = n.peer.onEvent
  n.peer.onEvent = (event, params) => {
    if (event === 'runtime.sessions') order.push(['list', params.sessions.map((s) => s.session_id)])
    orig(event, params)
  }
  n.resolve = (ix) => {
    order.push(['card', ix.session_id])
    return { allow: true }
  }
  rows = [{ session_id: 'new', status: 'working' }]
  const res = await rt.ask({ id: 'toy-1', kind: 'permission', session_id: 'new', tool_name: 'Bash', tool_input: {} })
  assert.deepEqual(res, { allow: true })
  assert.deepEqual(order.slice(0, 2), [['list', ['new']], ['card', 'new']])
})

test('stop() then start() in the same tick dials once more, not again and again (dialing mode)', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  assert.equal(n.hellos, 1)
  rt.stop()
  rt.start() // as a runtime restarting itself does (Paseo's version changed)
  await new Promise((r) => setTimeout(r, 1000))
  assert.equal(n.hellos, 2, 'the old connection\'s close and tail do not dial again')
})

test('stop() then start() while hello is in flight: the failed hello of the old run does not dial again', async (t) => {
  const n = await startFakeNode(t)
  n.holdHello = new Promise(() => {}) // the first hello is never answered
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await until('hello sent', () => n.hellos === 1)
  n.holdHello = undefined // the next one is answered
  rt.stop()
  rt.start()
  await n.ready
  await new Promise((r) => setTimeout(r, 1000))
  assert.equal(n.hellos, 2, 'the old hello failing does not dial again')
})

test('stop() before the dial has connected: the connection is dropped, not taken (dialing mode)', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  rt.stop() // the socket is still connecting
  await new Promise((r) => setTimeout(r, 300))
  assert.equal(n.hellos, 0, 'a stopped runtime says no hello')
  assert.equal(rt.connected, false)
})

test('a card for a new session waits for a slow list already under way, then for the list with it', async (t) => {
  const n = await startFakeNode(t)
  let rows = []
  let slow = false
  const listSessions = async () => {
    const now = rows // what the list is when it is read
    if (slow) {
      slow = false // only the push under way is slow
      await new Promise((r) => setTimeout(r, 6000)) // longer than the 5 s listNow used to wait
    }
    return now
  }
  const rt = createRuntime(minimal({ listSessions }), { runtime: 'toy', socketPath: n.path, retryMs: 20, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('the first list', () => n.last('runtime.sessions'))
  slow = true
  rt.sessionsChanged(0) // a slow push starts, before the session is there
  await new Promise((r) => setTimeout(r, 50))
  const order = []
  const orig = n.peer.onEvent
  n.peer.onEvent = (event, params) => {
    if (event === 'runtime.sessions') order.push(['list', params.sessions.map((s) => s.session_id)])
    orig(event, params)
  }
  n.resolve = (ix) => {
    order.push(['card', ix.session_id])
    return { allow: true }
  }
  rows = [{ session_id: 'new', status: 'working' }]
  const res = await rt.ask({ id: 'toy-2', kind: 'permission', session_id: 'new', tool_name: 'Bash', tool_input: {} })
  assert.deepEqual(res, { allow: true })
  const card = order.findIndex((e) => e[0] === 'card')
  assert.ok(order.slice(0, card).some((e) => e[0] === 'list' && e[1].includes('new')), JSON.stringify(order))
})


test('native rewind is advertised and returns the driver result with a refreshed inventory', async (t) => {
  const node = await startFakeNode(t)
  let called
  const result = { prompt: 'Example', restore: 'conversation', input_cleared: true, code_restored: false }
  const rt = createRuntime(minimal({ listSessions: async () => [{ session_id: 's1', status: 'idle', rev: called ? 2 : 1 }], rewind: async (params) => { called = params; return result } }), { runtime: 'toy', socketPath: node.path, pushDelayMs: 5 })
  t.after(() => rt.stop())
  rt.start()
  await node.ready
  assert.equal(node.hello.caps.rewind, true)
  await until('initial rewind inventory', () => node.last('runtime.sessions'))
  const params = { session_id: 's1', prompt: 'Example', occurrence: 1, prompts_after: 0, restore: 'conversation' }
  assert.deepEqual(await node.peer.call('session.rewind', params), result)
  assert.deepEqual(called, params)
  await until('inventory after rewind', () => node.all('runtime.sessions').length > 1)
})

test('seen is advertised only by a driver that has it, and reaches the driver (#6299)', async (t) => {
  const node = await startFakeNode(t)
  const seen = []
  const rt = createRuntime(minimal({ listSessions: async () => [{ session_id: 's1', status: 'done', rev: 1 }], seen: async (params) => { seen.push(params) } }),
    { runtime: 'toy', socketPath: node.path, pushDelayMs: 5 })
  t.after(() => rt.stop())
  rt.start()
  await node.ready
  assert.equal(node.hello.caps.seen, true)
  assert.deepEqual(await node.peer.call('session.seen', { session_id: 's1' }), { ok: true })
  assert.deepEqual(seen, [{ session_id: 's1' }])

  const other = await startFakeNode(t)
  const plain = createRuntime(minimal({}), { runtime: 'toy2', socketPath: other.path, pushDelayMs: 5 })
  t.after(() => plain.stop())
  plain.start()
  await other.ready
  assert.equal(other.hello.caps.seen, undefined, 'not said without the method')
  await assert.rejects(other.peer.call('session.seen', { session_id: 's1' }), /unsupported/)
})

test('runtime_driven_from_the_node_makes_the_phone_answer_first', async (t) => {
  // The session was started from Persona through another runtime (pi's companion starts it in pi-web): the node tells
  // the runtime running it, which then puts its cards on the phone first.
  const n = await startFakeNode(t)
  let asked
  const rt = createRuntime(minimal({ hasLocalClient: (id) => { asked = id; return true } }), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('list', () => n.last('runtime.sessions'))
  assert.equal(rt.phoneFirst('s1'), false, 'a local UI is open and the phone never drove it')
  assert.equal(asked, 's1', 'the driver is asked about that session')
  n.peer.notify('runtime.driven', { session_id: 's1' })
  await until('driven', () => rt.phoneFirst('s1'))
  assert.equal(rt.touched('s1'), true)
})

test('waiting_on_user_lists_the_session_blocked_until_released', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5, refreshMs: 0 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('idle', () => n.last('runtime.sessions')?.sessions[0].status === 'idle')
  const release = rt.waitingOnUser('s1')
  await until('blocked', () => n.last('runtime.sessions')?.sessions[0].status === 'blocked')
  assert.equal(rt.busy(), true, 'a question waiting holds a version switch too')
  release()
  release() // twice: once
  await until('idle again', () => n.last('runtime.sessions')?.sessions[0].status === 'idle')
  assert.equal(rt.busy(), false)
})

test('send_and_interrupt_say_queued_and_settled_as_the_driver_does', async (t) => {
  const n = await startFakeNode(t)
  let queue = false
  let settle = false
  const rt = createRuntime(minimal({
    send: async () => (queue ? { queued: true } : undefined),
    interrupt: async () => (settle ? { settled: true } : undefined),
  }), { runtime: 'toy', socketPath: n.path, retryMs: 20, pushDelayMs: 5 })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  assert.deepEqual(await n.peer.call('session.send', { session_id: 's1', text: 'a' }), { ok: true })
  queue = true
  assert.deepEqual(await n.peer.call('session.send', { session_id: 's1', text: 'b' }), { ok: true, queued: true })
  assert.deepEqual(await n.peer.call('session.interrupt', { session_id: 's1' }), { ok: true }, 'a stop not confirmed says nothing more')
  settle = true
  assert.deepEqual(await n.peer.call('session.interrupt', { session_id: 's1' }), { ok: true, settled: true })
})

test('supervision_policy and assistant_environment are explicit assistant-only capabilities', async (t) => {
  const full = await startFakeNode(t)
  const capable = createRuntime(
    minimal({ start: async () => ({ session: { session_id: 's1', status: 'idle' } }), send: async () => {} }),
    { runtime: 'agy-like', socketPath: full.path, retryMs: 20, assistant: true, supervisionPolicy: true, assistantEnvironment: true },
  )
  t.after(() => capable.stop())
  capable.start()
  await full.ready
  assert.equal(full.hello.caps.assistant, true)
  assert.equal(full.hello.caps.supervision_policy, true)
  assert.equal(full.hello.caps.assistant_environment, true)

  const legacy = await startFakeNode(t)
  const plainAsst = createRuntime(
    minimal({ start: async () => ({ session: { session_id: 's1', status: 'idle' } }), send: async () => {} }),
    { runtime: 'dsh-like', socketPath: legacy.path, retryMs: 20, assistant: true },
  )
  t.after(() => plainAsst.stop())
  plainAsst.start()
  await legacy.ready
  assert.equal(legacy.hello.caps.assistant, true)
  assert.equal(legacy.hello.caps.supervision_policy, undefined, 'absent unless explicitly opted in')
  assert.equal(legacy.hello.caps.assistant_environment, undefined, 'environment is absent unless explicitly opted in')

  const noAsst = await startFakeNode(t)
  const nonAsst = createRuntime(
    minimal({ start: async () => ({ session: { session_id: 's1', status: 'idle' } }), send: async () => {} }),
    { runtime: 'plain', socketPath: noAsst.path, retryMs: 20, assistant: false, supervisionPolicy: true, assistantEnvironment: true },
  )
  t.after(() => nonAsst.stop())
  nonAsst.start()
  await noAsst.ready
  assert.equal(noAsst.hello.caps.assistant, false)
  assert.equal(noAsst.hello.caps.supervision_policy, undefined, 'never advertised without assistant')
  assert.equal(noAsst.hello.caps.assistant_environment, undefined)

  const noSend = await startFakeNode(t)
  const incomplete = createRuntime(
    minimal({ start: async () => ({ session: { session_id: 's1', status: 'idle' } }) }),
    { runtime: 'half-asst', socketPath: noSend.path, retryMs: 20, assistant: true, supervisionPolicy: true, assistantEnvironment: true },
  )
  t.after(() => incomplete.stop())
  incomplete.start()
  await noSend.ready
  assert.equal(noSend.hello.caps.assistant, false)
  assert.equal(noSend.hello.caps.supervision_policy, undefined, 'never advertised when start/send are incomplete')
  assert.equal(noSend.hello.caps.assistant_environment, undefined)
})

test('hello carries codex_claims only when codexClaims names an app-server instance (#12571)', async (t) => {
  const plain = await startFakeNode(t)
  const unclaimed = createRuntime(minimal(), { runtime: 'plain', socketPath: plain.path, retryMs: 20 })
  t.after(() => unclaimed.stop())
  unclaimed.start()
  await plain.ready
  assert.equal(plain.hello.codex_claims, undefined, 'absent without the option')

  const n = await startFakeNode(t, { features: ['prefer_running', 'codex_claims'] })
  let asked = 0
  const rt = createRuntime(minimal(), {
    runtime: 'codexd',
    socketPath: n.path,
    retryMs: 20,
    codexClaims: () => {
      asked++
      return [{ home: '/srv/codex', endpoint: 'unix:///srv/codex/app.sock' }, { endpoint: 'ws://127.0.0.1:4500', note: 'x' }, { home: 7 }, null, {}]
    },
  })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  assert.deepEqual(n.hello.codex_claims, [{ home: '/srv/codex', endpoint: 'unix:///srv/codex/app.sock' }, { endpoint: 'ws://127.0.0.1:4500' }],
    'only the entries that name a home or an endpoint, with nothing else')
  assert.equal(asked, 1, 'a function is asked once per hello')
  await until('the node took the claims', () => rt.nodeHas('codex_claims'))

  const old = await startFakeNode(t)
  const listed = createRuntime(minimal(), { runtime: 'listed', socketPath: old.path, retryMs: 20, codexClaims: [{ home: '/srv/codex' }] })
  t.after(() => listed.stop())
  listed.start()
  await old.ready
  assert.deepEqual(old.hello.codex_claims, [{ home: '/srv/codex' }], 'a list is sent as it is')
  assert.equal(listed.nodeHas('codex_claims'), false, 'a node without the feature ignores the claims')

  for (const codexClaims of [[], [{}], () => { throw new Error('not yet') }]) {
    const none = await startFakeNode(t)
    const logged = []
    const quiet = createRuntime(minimal(), { runtime: 'none', socketPath: none.path, retryMs: 20, codexClaims, log: (line) => logged.push(line) })
    t.after(() => quiet.stop())
    quiet.start()
    await none.ready
    assert.equal(none.hello.codex_claims, undefined, 'nothing to claim sends no codex_claims')
    if (typeof codexClaims === 'function') assert.ok(logged.some((line) => line.includes('codexClaims: not yet')), 'a failing function is logged')
  }
})

test('compactionSettled notifies the node with session_id and optional error', async (t) => {
  const n = await startFakeNode(t)
  const rt = createRuntime(minimal(), { runtime: 'toy', socketPath: n.path, retryMs: 20, refreshMs: 0 })
  t.after(() => rt.stop())
  assert.equal(rt.compactionSettled('s1'), false)
  assert.equal(rt.compactionSettled(''), false)
  assert.equal(rt.compactionSettled(null), false)
  await rt.start()
  await n.ready
  assert.equal(rt.compactionSettled('s1'), true)
  assert.equal(rt.compactionSettled('s1', 'boom'), true)
  await until('both compaction settlement events received', () => n.all('session.compaction.settled').length === 2)
  assert.deepEqual(n.all('session.compaction.settled'), [
    { session_id: 's1' },
    { session_id: 's1', error: 'boom' },
  ])
})
