import { test } from 'node:test'
import assert from 'node:assert/strict'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createDshFiles } from '../lib/files.js'

const HOME = '/tmp/dsh-home-test'
const text = (seq, role, body) => role === 'user'
  ? { type: 'user/message', seq, time: 0, surfaceOp: 'append', data: { role: 'user', id: `u${seq}`, content: [{ type: 'text', text: body }], source: { kind: 'user' } } }
  : { type: 'assistant/message', seq, time: 0, surfaceOp: 'append', data: { message: { role: 'assistant', content: [{ type: 'text', text: body }] } } }

/** DSH's store with some sessions, read-only (what the companion may call). */
function store() {
  const s = {
    sessions: new Map(),
    reads: 0,
    add(id, header, events = [], at = Date.now()) {
      s.sessions.set(id, { header: { id, version: 4, createdAt: 1000, cwd: '/w/app', ...header }, events, at })
    },
    async list() {
      return [...s.sessions.values()].map((x) => ({ header: x.header, location: { kind: 'jsonl', path: `/store/${x.header.id}` } }))
    },
    async open(id, access) {
      assert.equal(access, 'read')
      const x = s.sessions.get(id)
      if (!x) throw new Error('not found')
      return { async read() { s.reads++; return { events: x.events } }, async close() {} }
    },
  }
  return s
}

async function setup(t, opts = {}) {
  const n = await startFakeNode(t, { features: opts.features })
  const st = store()
  const json = new Map(Object.entries(opts.json ?? {}))
  const wakes = []
  const rt = createDshFiles({
    persistence: st,
    readJson: (path) => json.get(path),
    mtime: (path) => st.sessions.get(path.replace('/store/', ''))?.at,
    wake: opts.noWake ? undefined : async () => { wakes.push(Date.now()) },
  }, { home: HOME, socketPath: n.path, pushDelayMs: 5, retryMs: 20, diskEveryMs: 0 })
  t.after(() => rt.stop())
  return { n, st, rt, wakes, json }
}

test('lists the recent sessions in the store as offline DSH rows, titled from DSH\'s cache', async (t) => {
  const { n, st, rt } = await setup(t, { json: {
    [`${HOME}/storages/workspace.json`]: { global: { archivedSessionIds: ['session-archived'] } },
    [`${HOME}/storages/session_projcache/sessions/session-a.json`]: { record: { identity: { createdAt: 1000, cwd: '/w/app' },
      rows: { title: { val: 'Fix the build' }, turnOutline: { val: { turns: [{ prompt: 'go', response: 'Built.' }] } }, turnBoundary: { seq: 7 } } } },
    [`${HOME}/storages/session_projcache/sessions/session-b.json`]: { record: { identity: { createdAt: 999, cwd: '/w/app' }, rows: { title: { val: 'stale' } } } },
  } })
  st.add('session-a', {})
  st.add('session-b', {})
  st.add('session-child', { origin: 'subagent', parentSession: 'session-a' })
  st.add('session-archived', {})
  st.add('session-old', {}, [], Date.now() - 30 * 86_400_000)
  rt.start()
  await n.ready
  assert.equal(n.hello.runtime, 'dsh-files')
  assert.equal(n.hello.agent, 'dsh')
  assert.deepEqual(n.hello.caps, { start: false, send: true, interrupt: false, queue_edit: false, queue_send_now: false, rename: false, close: false, assistant: false })
  await until('listed', () => n.last('runtime.sessions'))
  const rows = n.last('runtime.sessions').sessions
  assert.deepEqual(rows.map((r) => r.session_id).sort(), ['session-a', 'session-b'])
  const a = rows.find((r) => r.session_id === 'session-a')
  assert.equal(a.status, 'offline')
  assert.equal(a.title, 'Fix the build')
  assert.equal(a.last_text, 'Built.')
  assert.equal(a.rev, 8)
  assert.equal(a.agent_label, 'DSH')
  assert.equal(a.space_name, 'app')
  assert.equal(rows.find((r) => r.session_id === 'session-b').title, 'app', 'a cache record of another log is not used')
})

test('offline turn outlines skip marker-only replies on the first inventory', async (t) => {
  const marker = 'BOTMUX_NOTHING_TO_SEND'
  const words = `  The guide mentions ${marker}.\n  ${'detail '.repeat(45)}`
  const cases = [
    ['prior', [words, marker, `  ${marker}  `], words],
    ['mixed', [`${words}\n${marker}`], words],
    ['empty', [words, '', marker], undefined],
    ['missing', [words, undefined, marker], undefined],
  ]
  const cache = {}
  for (const [id, responses] of cases) {
    cache[`${HOME}/storages/session_projcache/sessions/${id}.json`] = { record: {
      identity: { createdAt: 1000, cwd: '/w/app' },
      rows: { turnOutline: { val: { turns: responses.map(response => ({ response })) } } },
    } }
  }
  const { n, st, rt } = await setup(t, { json: cache })
  for (const [id] of cases) st.add(id, {})
  rt.start()
  await n.ready
  const { sessions } = await until('disk inventory', () => n.last('runtime.sessions'))
  for (const [id, , expected] of cases) {
    assert.equal(sessions.find(row => row.session_id === id).last_text, expected, id)
  }
})

test('pages a session from its log, read once while the file does not change', async (t) => {
  const { n, st, rt } = await setup(t)
  st.add('session-p', {}, [text(0, 'user', 'one'), text(1, 'assistant', 'a1'), text(2, 'user', 'two'), text(3, 'assistant', 'a2')])
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  const latest = await n.peer.call('session.items', { session_id: 'session-p', turns: 1 })
  assert.deepEqual(latest.items.map((i) => i.text), ['two', 'a2'])
  assert.equal(latest.history, 'dsh:session-p', 'the plugin\'s history: the chat goes on when it takes over')
  assert.equal(latest.to_seq, 4)
  const after = await n.peer.call('session.items', { session_id: 'session-p', after_seq: 2 })
  assert.deepEqual(after.items.map((i) => i.text), ['two', 'a2'])
  assert.equal(st.reads, 1)
  await assert.rejects(n.peer.call('session.items', { session_id: 'session-none', limit: 1 }), /没有会话/)
})

test('session.items preserves full_size and avoids double-truncation on long tool outputs and messages', async (t) => {
  const { n, st, rt } = await setup(t)
  const longOutput = 'x'.repeat(10_000)
  const longReply = 'y'.repeat(30_000)
  st.add('session-long', {}, [
    text(0, 'user', 'run it'),
    { type: 'tool/result', seq: 1, time: 0, surfaceOp: 'append', data: { message: { toolCallId: 'c1', content: [{ type: 'text', text: longOutput }], isError: false } } },
    text(2, 'assistant', longReply),
  ])
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  const res = await n.peer.call('session.items', { session_id: 'session-long' })
  const toolItem = res.items.find((i) => i.kind === 'tool_result')
  const replyItem = res.items.find((i) => i.role === 'assistant' && i.kind === 'text')
  assert.equal(toolItem.full_size, longOutput.length)
  assert.equal((toolItem.text.match(/已截断/g) ?? []).length, 1)
  assert.ok(toolItem.text.endsWith(`共 ${longOutput.length} 字节）`))
  assert.equal(replyItem.full_size, longReply.length)
  assert.equal((replyItem.text.match(/已截断/g) ?? []).length, 1)
  assert.ok(replyItem.text.endsWith(`共 ${longReply.length} 字节）`))
})

test('a send wakes DSH once and is handed over, nothing delivered here', async (t) => {
  const { n, st, rt, wakes } = await setup(t)
  st.add('session-s', {})
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  for (let i = 0; i < 2; i++) {
    await assert.rejects(n.peer.call('session.send', { session_id: 'session-s', text: 'hi', client_msg_id: `m${i}` }),
      (err) => err.code === 'handover' && err.data?.delivered === 'none')
  }
  assert.equal(wakes.length, 1, 'DSH is started once, not on every message')
})

test('it stays off a node that cannot hand a send over', async (t) => {
  const { n, rt } = await setup(t, { features: ['prefer_running', 'resume_argv'] })
  rt.start()
  await until('hello', () => n.hellos >= 1)
  await new Promise((r) => setTimeout(r, 100))
  assert.equal(rt.connected, false)
  assert.equal(n.last('runtime.sessions'), undefined)
})
