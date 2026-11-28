import { mcpToolNames } from '../../runtime-sdk/lib/compat.js'
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { answerOf, createDshRuntime } from '../lib/dsh.js'

/** A Cordis-like event bus: emit for emit events, waterfall for the answerer chains (registration order, prepend first). */
function bus() {
  const listeners = new Map()
  return {
    on(name, fn, opts = {}) {
      const list = listeners.get(name) ?? []
      if (opts.prepend) list.unshift(fn)
      else list.push(fn)
      listeners.set(name, list)
      return () => listeners.set(name, (listeners.get(name) ?? []).filter((f) => f !== fn))
    },
    emit(name, ...args) {
      for (const fn of listeners.get(name) ?? []) fn(...args)
    },
    waterfall(name, payload, last) {
      const list = listeners.get(name) ?? []
      const run = (i) => (i < list.length ? Promise.resolve(list[i](payload, () => run(i + 1))) : Promise.resolve(last()))
      return run(0)
    },
  }
}

function deferred() {
  let resolve, reject
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

/** A sessionController over in-memory logs. */
function controller() {
  const sessions = new Map()
  const c = {
    sessions,
    prompts: [],
    cancels: [],
    renames: [],
    add(id, summary, events = [], values = {}) {
      sessions.set(id, { summary: { sessionId: id, updatedAt: Date.now(), running: false, blank: false, agentAvailable: true, ...summary }, events, values })
    },
    async list() {
      return { items: [...sessions.values()].map((s) => s.summary) }
    },
    async projections({ sessionId }) {
      const s = sessions.get(sessionId)
      return s ? { asOfSeq: s.events.at(-1)?.seq ?? -1, values: s.values ?? {} } : null
    },
    pages: [],
    selectedModels: [],
    async selectModel(req) {
      c.selectedModels.push({ ...req, at: Date.now() })
      return { ok: true }
    },
    async page({ address, throughSeq, beforeSeq, maxMessages }) {
      c.pages.push(address)
      // DSH pages a subagent only through its parent's address, and refuses its plain one.
      const id = address.kind === 'subagent' ? address.childSessionId : address.sessionId
      const s = sessions.get(id)
      if (address.kind === 'subagent' && s?.summary.parentSessionId !== address.parentSessionId) throw new Error('subagent/unauthorized')
      if (address.kind === 'session' && s?.summary.origin === 'subagent') throw new Error('session/agent-busy')
      const inRange = s.events.filter((e) => e.seq <= throughSeq && (beforeSeq === undefined || e.seq < beforeSeq))
      const page = inRange.slice(-maxMessages)
      return { records: page.map((event) => ({ type: 'event', event })), hasMore: page.length < inRange.length }
    },
    async prompt(req) {
      c.prompts.push(req)
      return { accepted: true }
    },
    cancel(req) {
      c.cancels.push(req)
      return { accepted: true }
    },
    async create({ cwd }) {
      c.add('session-new', { cwd, blank: true })
      return { sessionId: 'session-new' }
    },
    async rename(req) {
      c.renames.push(req)
      return { title: req.title }
    },
    async attachment() {
      return { attachment: { mediaType: 'image/png' }, data: 'iVBORw0KGgo=' }
    },
  }
  return c
}

const text = (seq, role, body) => role === 'user'
  ? { type: 'user/message', seq, time: 0, surfaceOp: 'append', data: { role: 'user', id: `u${seq}`, content: [{ type: 'text', text: body }], source: { kind: 'user' } } }
  : { type: 'assistant/message', seq, time: 0, surfaceOp: 'append', data: { message: { role: 'assistant', content: [{ type: 'text', text: body }] } } }

async function setup(t, opts = {}) {
  const n = await startFakeNode(t, { tools: opts.tools, features: opts.features })
  const ctl = controller()
  const ev = bus()
  const answered = []
  const rt = createDshRuntime({
    controller: ctl,
    on: ev.on,
    userQuestions: { answer: (agent, callId, answer) => { answered.push({ callId, answer }); return true } },
    hasLiveClient: () => opts.liveClient ?? true,
    workspace: opts.workspace,
    registerTool: opts.registerTool,
    agents: opts.agents,
    permissionPresets: opts.permissionPresets,
    jobs: opts.jobs,
    models: opts.models,
  }, { socketPath: n.path, pushDelayMs: 5, retryMs: 20, interactions: opts.interactions, maxSessions: opts.maxSessions,
    runtime: opts.runtime, name: opts.name, launcher: opts.launcher, assistant: opts.assistant, presets: opts.presets })
  t.after(() => rt.stop())
  return { n, ctl, ev, rt, answered }
}

test('lists DSH sessions to the node after hello', async (t) => {
  const { n, ctl, rt } = await setup(t)
  ctl.add('session-a', { cwd: '/w/app', running: true, projections: { kind: 'cached', asOfSeq: 41, values: { title: 'Fix the build' } } })
  ctl.add('session-blank', { cwd: '/w/app', blank: true })
  ctl.add('session-child', { cwd: '/w/app', origin: 'subagent' })
  ctl.add('session-legacy-child', { cwd: '/w/app', parentSessionId: 'session-a' }) // older DSH: a parent, no origin
  ctl.add('session-old', { cwd: '/w/app', updatedAt: Date.now() - 30 * 86_400_000 })
  rt.start()
  await n.ready
  assert.equal(n.hello.runtime, 'dsh')
  assert.equal(n.hello.name, 'DSH')
  assert.equal(n.hello.launcher, 'dsh')
  assert.equal(n.hello.caps.close, false)
  assert.equal(n.hello.caps.send, true)
  await until('runtime.sessions', () => n.last('runtime.sessions'))
  const list = n.last('runtime.sessions').sessions
  assert.deepEqual(list.map((s) => s.session_id), ['session-a'])
  assert.equal(list[0].status, 'working')
  assert.equal(list[0].title, 'Fix the build')
  assert.equal(list[0].space_name, 'app')
  assert.equal(list[0].rev, 42)
})

test('a session the phone sends to stays listed past the count and age limits', async (t) => {
  const { n, ctl, rt } = await setup(t, { maxSessions: 2 })
  ctl.add('session-old', { cwd: '/w/app', updatedAt: Date.now() - 30 * 86_400_000 }, [text(0, 'user', 'long ago')])
  for (const id of ['session-x', 'session-y', 'session-z']) ctl.add(id, { cwd: '/w/app' })
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(n.last('runtime.sessions').sessions.length, 2)
  assert.ok(!n.last('runtime.sessions').sessions.some((s) => s.session_id === 'session-old'))
  await n.peer.call('session.send', { session_id: 'session-old', text: 'still there?' })
  await until('kept in the list', () => n.last('runtime.sessions')?.sessions.some((s) => s.session_id === 'session-old'))
  assert.equal(n.last('runtime.sessions').sessions.length, 2, 'the kept one counts toward the limit')
})

test('pages history back from the end, by turns, before a seq, after a seq and over a range', async (t) => {
  const { n, ctl, rt } = await setup(t)
  ctl.add('session-p', {}, [text(0, 'user', 'one'), text(1, 'assistant', 'a1'), text(2, 'user', 'two'), text(3, 'assistant', 'a2')])
  rt.start()
  await n.ready
  const latest = await n.peer.call('session.items', { session_id: 'session-p', turns: 2 })
  assert.deepEqual(latest.items.map((i) => i.text), ['one', 'a1', 'two', 'a2'])
  assert.equal(latest.history, 'dsh:session-p')
  assert.equal(latest.to_seq, 4)
  const before = await n.peer.call('session.items', { session_id: 'session-p', before_seq: 3, limit: 1 })
  assert.deepEqual(before.items.map((i) => i.text), ['a1'])
  assert.equal(before.has_more, true)
  const after = await n.peer.call('session.items', { session_id: 'session-p', after_seq: 2 })
  assert.deepEqual(after.items.map((i) => i.text), ['two', 'a2'])
  assert.equal(after.from_seq, 2)
  assert.equal(after.to_seq, 4)
  const caughtUp = await n.peer.call('session.items', { session_id: 'session-p', after_seq: 4 })
  assert.deepEqual(caughtUp.items, [])
  const range = await n.peer.call('session.items', { session_id: 'session-p', from_seq: 2, through_seq: 3 })
  assert.deepEqual(range.items.map((i) => i.text), ['a1', 'two'])
  await assert.rejects(n.peer.call('session.items', { session_id: 'nope' }), /没有会话/)
})

test('catch-up over more than 2,000 DSH events begins at the requested cursor', async (t) => {
  const { n, ctl, rt } = await setup(t)
  const events = Array.from({ length: 2501 }, (_, seq) => text(seq, seq % 2 === 0 ? 'user' : 'assistant', `message ${seq}`))
  ctl.add('session-long', {}, events)
  rt.start()
  await n.ready

  const result = await n.peer.call('session.items', { session_id: 'session-long', after_seq: 100 })
  assert.equal(result.from_seq, 100, 'the batch must continue from the caller cursor')
  assert.equal(result.to_seq, 600, 'the first 500 item-producing events form the first page')
  assert.equal(result.more_after, true)
  assert.equal(result.items.length, 500)
  assert.equal(result.items[0].text, 'message 100')
  assert.equal(result.items.at(-1).text, 'message 599')
})

test('a bounded catch-up window does not jump over non-displayed records', async (t) => {
  const { n, ctl, rt } = await setup(t)
  const events = Array.from({ length: 2501 }, (_, seq) => ({ type: 'model/changed', seq, time: 0, data: {} }))
  events[2500] = text(2500, 'assistant', 'tail')
  ctl.add('session-long', {}, events)
  rt.start()
  await n.ready

  const first = await n.peer.call('session.items', { session_id: 'session-long', after_seq: 100 })
  assert.equal(first.from_seq, 100)
  assert.equal(first.to_seq, 2100)
  assert.equal(first.more_after, true)
  assert.deepEqual(first.items, [])

  const second = await n.peer.call('session.items', { session_id: 'session-long', after_seq: first.to_seq })
  assert.equal(second.from_seq, 2100)
  assert.equal(second.to_seq, 2501)
  assert.equal(second.more_after, false)
  assert.deepEqual(second.items.map((item) => item.text), ['tail'])
})

test('sends with the client message id, queued when idle and steered while working', async (t) => {
  const { n, ctl, ev, rt } = await setup(t)
  ctl.add('session-s', {}, [text(0, 'user', 'hi')])
  rt.start()
  await n.ready
  await n.peer.call('session.send', { session_id: 'session-s', text: 'go on', client_msg_id: 'm-1', images: [{ mime: 'image/png', data: 'AAAA' }] })
  assert.equal(ctl.prompts[0].requestId, 'm-1')
  assert.equal(ctl.prompts[0].mode, 'queue')
  assert.deepEqual(ctl.prompts[0].content, [{ type: 'text', text: 'go on' }, { type: 'image', mediaType: 'image/png', data: 'AAAA', name: 'phone' }])
  ev.emit('session/event', { id: 'session-s' }, { type: 'turn/start', seq: 1, data: { turn: 1 } })
  await n.peer.call('session.send', { session_id: 'session-s', text: 'also this' })
  assert.equal(ctl.prompts[1].mode, 'steer')
  await n.peer.call('session.interrupt', { session_id: 'session-s' })
  assert.deepEqual(ctl.cancels, [{ sessionId: 'session-s' }])
  await assert.rejects(n.peer.call('session.send', { session_id: 'session-s', text: '  ' }), /空/)
})

test('starts a session from the phone and lists it before its first prompt', async (t) => {
  const { n, ctl, rt } = await setup(t)
  rt.start()
  await n.ready
  const res = await n.peer.call('session.start', { cwd: '/w/new', prompt: 'hello' })
  assert.equal(res.session.session_id, 'session-new')
  assert.equal(ctl.prompts[0].sessionId, 'session-new')
  await until('listed', () => n.last('runtime.sessions')?.sessions.some((s) => s.session_id === 'session-new'))
})

// The real controller derives cwd from a workspaceId; retain that behavior while recording its public request.
function recordCreations(ctl, workspacePath) {
  const requests = [], create = ctl.create
  ctl.create = async (request) => {
    requests.push(request)
    return create({ cwd: request.cwd ?? workspacePath })
  }
  return requests
}

for (const selected of ['/w/project', '/w/project-alias']) test(`a new DSH session joins the workspace resolved for ${selected} (#5485)`, async (t) => {
  const matched = { id: 'workspace-project', path: '/w/project' }
  const ws = { ...workspace(), async resolveByPath(path) {
    return ['/w/project', '/w/project-alias'].includes(path) ? matched : undefined
  } }
  const { n, ctl, rt } = await setup(t, { workspace: ws })
  const requests = recordCreations(ctl, matched.path)
  rt.start()
  await n.ready
  const { session } = await n.peer.call('session.start', { cwd: selected, prompt: 'Check the synthetic project' })
  assert.deepEqual(requests, [{ workspaceId: matched.id }], 'DSH receives workspaceId alone so it attaches the new session')
  assert.equal(session.session_id, 'session-new')
  assert.equal(ctl.prompts[0].sessionId, session.session_id)
  assert.equal(ctl.sessions.get(session.session_id).summary.cwd, matched.path)
})

test('a directory without an exact DSH workspace keeps cwd-only creation (#5485)', async (t) => {
  const ws = { ...workspace(), async resolveByPath() { return undefined } }
  const { n, ctl, rt } = await setup(t, { workspace: ws })
  const requests = recordCreations(ctl)
  rt.start()
  await n.ready
  await n.peer.call('session.start', { cwd: '/w/unmatched' })
  assert.deepEqual(requests, [{ cwd: '/w/unmatched' }])
})

for (const [name, ws] of [['absent', undefined], ['archive-only', workspace()]]) test(`an ${name} workspace service preserves cwd-only creation (#5485)`, async (t) => {
  const { n, ctl, rt } = await setup(t, { workspace: ws })
  const requests = recordCreations(ctl)
  rt.start()
  await n.ready
  await n.peer.call('session.start', { cwd: '/w/new' })
  assert.deepEqual(requests, [{ cwd: '/w/new' }])
})

test('a new directory keeps the DSH controller creation path (#5485)', async (t) => {
  const ws = { ...workspace(), async resolveByPath() { throw Object.assign(new Error('directory does not exist'), { code: 'ENOENT' }) } }
  const { n, ctl, rt } = await setup(t, { workspace: ws })
  const requests = recordCreations(ctl)
  rt.start()
  await n.ready
  await n.peer.call('session.start', { cwd: '/w/new-directory' })
  assert.deepEqual(requests, [{ cwd: '/w/new-directory' }], 'the controller creates directories that do not exist yet')
})

test('a failed DSH workspace lookup creates no ungrouped session (#5485)', async (t) => {
  const ws = { ...workspace(), async resolveByPath() { throw new Error('workspace lookup unavailable') } }
  const { n, ctl, rt } = await setup(t, { workspace: ws })
  const requests = recordCreations(ctl)
  rt.start()
  await n.ready
  await assert.rejects(n.peer.call('session.start', { cwd: '/w/project', prompt: 'Do not create on failed lookup' }), /workspace lookup unavailable/)
  assert.deepEqual(requests, [])
  assert.equal(ctl.sessions.size, 0)
  assert.deepEqual(ctl.prompts, [])
})

test('a watched session gets its new events as continuous items and the live text as deltas', async (t) => {
  const { n, ctl, ev, rt } = await setup(t)
  ctl.add('session-w', {}, [text(0, 'user', 'one'), text(1, 'assistant', 'a1')])
  rt.start()
  await n.ready
  await n.peer.call('session.watch', { session_id: 'session-w', since_seq: 1 })
  await until('catch-up', () => n.all('session.items').length === 1)
  const catchUp = n.all('session.items')[0]
  assert.deepEqual(catchUp.items.map((i) => i.text), ['a1'])
  assert.equal(catchUp.from_seq, 1)
  assert.equal(catchUp.to_seq, 2)

  const next = [text(2, 'user', 'two'), { type: 'step/start', seq: 3, data: {} }, text(4, 'assistant', 'a2')]
  for (const e of next) {
    ctl.sessions.get('session-w').events.push(e)
    ev.emit('session/event', { id: 'session-w' }, e)
  }
  await until('live batch', () => n.all('session.items').length === 2)
  const live = n.all('session.items')[1]
  assert.deepEqual(live.items.map((i) => i.text), ['two', 'a2'])
  assert.equal(live.from_seq, 2)
  assert.equal(live.to_seq, 5)

  const agent = { session: { id: 'session-w' } }
  ev.emit('agent/assistant-stream', { agent, frame: { type: 'start' } })
  ev.emit('agent/assistant-stream', { agent, frame: { type: 'chunk', chunk: { type: 'text-delta', text: 'Hel' } } })
  ev.emit('agent/assistant-stream', { agent, frame: { type: 'chunk', chunk: { type: 'text-delta', text: 'lo' } } })
  await until('delta', () => n.last('session.delta')?.text === 'Hello')
  ev.emit('agent/assistant-stream', { agent, frame: { type: 'end' } })
  await until('end of the delta', () => n.last('session.delta')?.text === '')

  // A missed event: the batch says where it starts, so the phone fills the gap.
  ev.emit('session/event', { id: 'session-w' }, text(9, 'assistant', 'later'))
  await until('gap batch', () => n.all('session.items').length === 3)
  assert.equal(n.all('session.items')[2].from_seq, 9)
})

test('a watch that starts mid-message gets the whole live text so far, not just what came after', async (t) => {
  const { n, ctl, ev, rt } = await setup(t)
  ctl.add('session-m', {}, [text(0, 'user', 'one')])
  rt.start()
  await n.ready
  const agent = { session: { id: 'session-m' } }
  ev.emit('agent/assistant-stream', { agent, frame: { type: 'start' } })
  ev.emit('agent/assistant-stream', { agent, frame: { type: 'chunk', chunk: { type: 'text-delta', text: 'Hel' } } }) // nobody watching yet
  await n.peer.call('session.watch', { session_id: 'session-m', since_seq: 0 })
  ev.emit('agent/assistant-stream', { agent, frame: { type: 'chunk', chunk: { type: 'text-delta', text: 'lo' } } })
  await until('delta', () => n.all('session.delta').length > 0)
  assert.equal(n.last('session.delta').text, 'Hello')
})

async function firstInteractionAfterAdmission(t, kind, { automatic = false, beforeStart = false } = {}) {
  const n = await startFakeNode(t, { strict: true })
  const ev = bus()
  const sessions = new Map()
  let browserAnswers = 0
  let inventoryAtCard
  let promptAdmitted
  const admitted = new Promise((resolve) => { promptAdmitted = resolve })
  const ctl = {
    async list() { return { items: [...sessions.values()] } },
    async create({ cwd }) {
      sessions.set('session-new', { sessionId: 'session-new', cwd, updatedAt: Date.now(), running: false, blank: true })
      return { sessionId: 'session-new' }
    },
    async prompt() {
      // The real controller acknowledges inbox admission, rather than holding prompt open until the model turn ends.
      sessions.get('session-new').blank = false
      promptAdmitted()
      return { accepted: true }
    },
  }
  const rt = createDshRuntime({ controller: ctl, on: ev.on, hasLiveClient: () => true },
    { socketPath: n.path, pushDelayMs: 5000, retryMs: 20, interactions: automatic ? undefined : 'phone' })
  t.after(() => rt.stop())
  rt.start()
  await n.ready
  await until('initial empty inventory', () => n.last('runtime.sessions'))
  const starting = n.peer.call('session.start', { cwd: '/w/new', prompt: 'run the first tool' })
  let startSettled = false
  starting.then(() => { startSettled = true }, () => { startSettled = true })
  if (beforeStart) {
    await admitted
    assert.equal(startSettled, false, 'the initial prompt settled, but the outer session.start RPC has not')
  } else {
    await starting
  }
  assert.ok(!n.last('runtime.sessions').sessions.some((s) => s.session_id === 'session-new'), 'the ordinary inventory push is still delayed')

  const agent = { session: { id: 'session-new' } }
  const plan = { id: 'review', question: 'Approve?', detail: '# Plan\n1. do it', options: [{ label: 'Approve' }, { label: 'Keep planning' }], intent: { kind: 'plan-review', approve: 'Approve' } }
  const question = { id: 'q1', question: 'Which db?', options: [{ label: 'pg' }, { label: 'sqlite' }] }
  const event = kind === 'permission' ? 'approval/request' : 'user-questions/request'
  const payload = kind === 'permission'
    ? { agent, toolName: 'bash', callId: 'first-call' }
    : { agent, questions: [kind === 'plan' ? plan : question] }
  n.resolve = () => {
    inventoryAtCard = n.last('runtime.sessions')
    if (kind === 'permission') return { allow: true }
    if (kind === 'plan') return { approve: true }
    return { answers: { 'Which db?': 'pg' } }
  }
  const fromBrowser = () => { browserAnswers++; return 'browser-answer' }
  const result = await ev.waterfall(event, payload, fromBrowser)
  if (beforeStart) await starting

  assert.equal(browserAnswers, 0, 'the first post-admission interaction must not fall through to the browser')
  assert.equal(n.asked.length, 1, 'exactly one phone card')
  assert.equal(n.refused.length, 0, 'the node saw the session before its card')
  assert.ok(inventoryAtCard?.sessions?.some((s) => s.session_id === 'session-new'), 'runtime.sessions containing the new id is published before interaction.create')
  assert.equal(n.asked[0].kind, kind)
  if (kind === 'permission') assert.equal(result, 'allowed-once')
  if (kind === 'plan') assert.deepEqual(result, { answers: [{ id: 'review', selected: ['Approve'] }] })
  if (kind === 'question') assert.deepEqual(result, { answers: [{ id: 'q1', selected: ['pg'] }] })
}

for (const [kind, label] of [['permission', 'approval'], ['question', 'question'], ['plan', 'plan review']]) {
  test(`the first ${label} after immediate prompt admission goes to the phone before delayed inventory (#8203)`, async (t) => {
    await firstInteractionAfterAdmission(t, kind)
  })
  test(`automatic mode keeps the after-start ${label} control on the phone (#8203)`, async (t) => {
    await firstInteractionAfterAdmission(t, kind, { automatic: true })
  })
  test(`automatic mode routes the first ${label} after prompt admission but before session.start resolves (#8203)`, async (t) => {
    await firstInteractionAfterAdmission(t, kind, { automatic: true, beforeStart: true })
  })
}

for (const [label, error] of [
  ['failure', new Error('initial prompt failed')],
  ['cancellation', Object.assign(new Error('initial prompt cancelled'), { name: 'AbortError' })],
]) {
  test(`automatic mode leaves no phone-driven eligibility after initial prompt ${label} (#8203)`, async (t) => {
    const n = await startFakeNode(t, { strict: true })
    const ev = bus()
    const sessions = new Map()
    const ctl = {
      async list() { return { items: [...sessions.values()] } },
      async create({ cwd }) {
        sessions.set('session-new', { sessionId: 'session-new', cwd, updatedAt: Date.now(), running: false, blank: true })
        return { sessionId: 'session-new' }
      },
      async prompt() { throw error },
    }
    const rt = createDshRuntime({ controller: ctl, on: ev.on, hasLiveClient: () => true },
      { socketPath: n.path, pushDelayMs: 5000, retryMs: 20 })
    t.after(() => rt.stop())
    rt.start()
    await n.ready
    await until('initial empty inventory', () => n.last('runtime.sessions'))
    await assert.rejects(n.peer.call('session.start', { cwd: '/w/new', prompt: 'run the first tool' }), new RegExp(error.message))

    let browserAnswers = 0
    const result = await ev.waterfall('approval/request', {
      agent: { session: { id: 'session-new' } }, toolName: 'bash', callId: 'after-failed-start',
    }, () => { browserAnswers++; return 'browser-answer' })
    assert.equal(result, 'browser-answer')
    assert.equal(browserAnswers, 1, 'an unknown unlisted automatic session stays with its local UI')
    assert.equal(n.asked.length, 0, 'a failed start leaves no phantom phone card')
    assert.equal(n.refused.length, 0)
    assert.ok(!n.last('runtime.sessions').sessions.some((s) => s.session_id === 'session-new'))
  })
}

test('approvals of a session the phone drives go to the phone first; a dismissed card goes to the browser', async (t) => {
  const { n, ctl, ev, rt } = await setup(t)
  ctl.add('session-x', {}, [text(0, 'user', 'deploy')])
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  const agent = { session: { id: 'session-x' } }
  ev.emit('session/event', { id: 'session-x' }, { type: 'assistant/message', seq: 1, data: { message: { content: [
    { type: 'tool-call', id: 'call_1', name: 'bash', arguments: '{"command":"rm -rf build"}' }] } } })

  // Not driven from the phone and a browser is open: the browser decides, nothing reaches the phone.
  let browser = 0
  const fromBrowser = () => { browser++; return 'rejected' }
  assert.equal(await ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'call_1' }, fromBrowser), 'rejected')
  assert.equal(n.asked.length, 0)

  await n.peer.call('session.send', { session_id: 'session-x', text: 'yes, deploy' })
  n.resolve = () => ({ allow: true })
  assert.equal(await ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'call_1' }, fromBrowser), 'allowed-once')
  assert.equal(n.asked.length, 1)
  assert.equal(n.asked[0].kind, 'permission')
  assert.equal(n.asked[0].tool_name, 'Bash')
  assert.deepEqual(n.asked[0].tool_input, { command: 'rm -rf build' })
  assert.equal(n.asked[0].summary, 'rm -rf build')
  assert.equal(browser, 1)

  n.resolve = () => ({ dismiss: true })
  assert.equal(await ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'call_2' }, fromBrowser), 'rejected')
  assert.equal(browser, 2)

  // DSH's reason goes beside the command; a refusal with words rejects, then steers the words into the turn.
  n.resolve = () => ({ allow: false, message: 'not on Friday' })
  const before = ctl.prompts.length
  assert.equal(await ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'call_1', reason: 'deletes files' }, fromBrowser), 'rejected')
  const card = n.asked.at(-1)
  assert.equal(card.reason, 'deletes files')
  assert.equal(card.summary, 'rm -rf build')
  await until('the reason steered in', () => ctl.prompts.length === before + 1)
  const steer = ctl.prompts.at(-1)
  assert.equal(steer.mode, 'steer')
  assert.equal(steer.sessionId, 'session-x')
  assert.match(steer.content[0].text, /拒绝了 Bash）not on Friday$/)
  assert.equal(steer.requestId, `${card.id}-reason`)
})

test('with no browser open the phone gets approvals; the browser policy never asks it', async (t) => {
  const { n, ctl, ev, rt } = await setup(t, { liveClient: false })
  ctl.add('session-y', {})
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  n.resolve = () => ({ allow: false })
  assert.equal(await ev.waterfall('approval/request', { agent: { session: { id: 'session-y' } }, toolName: 'write' }, () => 'allowed-once'), 'rejected')
  assert.equal(n.asked.length, 1)
})

test('plan reviews and questions go to the phone; a timed question answered late goes in through userQuestions.answer', async (t) => {
  const { n, ctl, ev, rt, answered } = await setup(t, { interactions: 'phone' })
  ctl.add('session-q', {})
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  const agent = { session: { id: 'session-q' } }
  const plan = { id: 'review', question: 'Approve?', detail: '# Plan\n1. do it', options: [{ label: 'Approve' }, { label: 'Keep planning' }], intent: { kind: 'plan-review', approve: 'Approve' } }
  n.resolve = () => ({ approve: true })
  const res = await ev.waterfall('user-questions/request', { agent, questions: [plan] }, () => { throw new Error('no answerer') })
  assert.deepEqual(res, { answers: [{ id: 'review', selected: ['Approve'] }] })
  assert.equal(n.asked[0].kind, 'plan')
  assert.equal(n.asked[0].plan, '# Plan\n1. do it')

  // The timed wait ends before the phone answers: DSH gets the timeout, the phone's answer arrives later.
  let release
  n.resolve = () => new Promise((r) => { release = r })
  const wait = new AbortController()
  const q = { id: 'q1', question: 'Which db?', options: [{ label: 'pg' }, { label: 'sqlite' }] }
  const pending = ev.waterfall('user-questions/request', { agent, questions: [q], signal: wait.signal, wait: { callId: 'call_q', timed: true } }, () => { throw new Error('no answerer') })
  await until('asked', () => n.asked.length === 2)
  assert.equal(n.asked[1].kind, 'question')
  assert.equal(n.asked[1].questions[0].question, 'Which db?')
  wait.abort(new Error('ASK_TIMED_OUT'))
  await assert.rejects(pending, /ASK_TIMED_OUT/)
  release({ answers: { 'Which db?': 'pg' } })
  await until('late answer', () => answered.length === 1)
  assert.deepEqual(answered[0], { callId: 'call_q', answer: { answers: [{ id: 'q1', selected: ['pg'] }] } })
})

test('both mode lets the phone win an approval, aborts Web, and keeps fallback behavior (#10176)', async (t) => {
  const { n, ctl, ev, rt } = await setup(t, { interactions: 'both' })
  ctl.add('session-both-a', {})
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  const agent = { session: { id: 'session-both-a' } }

  const phone = deferred()
  const web = deferred()
  const req = { agent, toolName: 'bash', callId: 'both-phone' }
  let webSignal
  n.resolve = () => phone.promise
  const promptsBefore = ctl.prompts.length
  const pending = ev.waterfall('approval/request', req, () => { webSignal = req.signal; return web.promise })
  await until('both answerers are active', () => n.asked.length === 1 && webSignal)
  phone.resolve({ allow: false, message: 'phone decision' })
  assert.equal(await pending, 'rejected')
  assert.equal(webSignal.aborted, true, 'the phone winner aborts the Web GUI request')
  await until('the phone refusal reason steered in', () => ctl.prompts.length === promptsBefore + 1)
  assert.equal(ctl.prompts.at(-1).requestId, `${n.asked[0].id}-reason`)
  web.reject(new Error('late Web failure'))
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(n.all('interaction.cancel').length, 0, 'an answered phone card is not withdrawn')

  const unavailablePhone = deferred()
  n.resolve = () => unavailablePhone.promise
  let unavailableSettled = false
  const unavailable = ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'both-unavailable' }, () => 'unavailable')
  unavailable.then(() => { unavailableSettled = true }, () => { unavailableSettled = true })
  await until('the second phone card', () => n.asked.length === 2)
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(unavailableSettled, false, 'an unavailable Web answerer does not win')
  assert.equal(n.all('interaction.cancel').length, 0)
  unavailablePhone.resolve({ allow: true })
  assert.equal(await unavailable, 'allowed-once')

  const dismissedWeb = deferred()
  const dismissedReq = { agent, toolName: 'bash', callId: 'both-dismissed' }
  let dismissedSignal
  n.resolve = () => ({ dismiss: true })
  const dismissed = ev.waterfall('approval/request', dismissedReq, () => { dismissedSignal = dismissedReq.signal; return dismissedWeb.promise })
  await until('the dismissed phone card', () => n.asked.length === 3)
  await new Promise((resolve) => setImmediate(resolve))
  dismissedWeb.resolve('allowed-once')
  assert.equal(await dismissed, 'allowed-once')
  assert.equal(dismissedSignal.aborted, false, 'a dismissed phone card leaves the Web branch alone')
})

test('both mode lets Web win an approval and withdraws both branches on cancellation (#10176)', async (t) => {
  const { n, ctl, ev, rt } = await setup(t, { interactions: 'both' })
  ctl.add('session-both-b', {})
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  const agent = { session: { id: 'session-both-b' } }

  const phone = deferred()
  const web = deferred()
  n.resolve = () => phone.promise
  const promptsBefore = ctl.prompts.length
  const pending = ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'both-web' }, () => web.promise)
  await until('the first phone card', () => n.asked.length === 1)
  web.resolve('rejected')
  assert.equal(await pending, 'rejected')
  await until('the Web winner withdraws the phone', () => n.last('interaction.cancel')?.id === n.asked[0].id)
  phone.resolve({ allow: true })
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(ctl.prompts.length, promptsBefore, 'a Web denial has no phone refusal reason to steer')

  const cancelledPhone = deferred()
  const turn = new AbortController()
  const req = { agent, toolName: 'bash', callId: 'both-cancelled', signal: turn.signal }
  let webSignal
  n.resolve = () => cancelledPhone.promise
  const cancelled = ev.waterfall('approval/request', req, () => {
    webSignal = req.signal
    return new Promise((resolve, reject) => req.signal.addEventListener('abort', () => reject(req.signal.reason), { once: true }))
  })
  await until('the second phone card', () => n.asked.length === 2)
  turn.abort(new Error('turn stopped'))
  assert.equal(await cancelled, 'cancelled')
  assert.equal(webSignal.aborted, true)
  await until('turn cancellation withdraws the phone', () => n.last('interaction.cancel')?.id === n.asked[1].id)
  cancelledPhone.resolve({ allow: true })

  const offlinePhone = deferred()
  const offlineWeb = deferred()
  n.resolve = () => offlinePhone.promise
  const offline = ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'both-offline' }, () => offlineWeb.promise)
  await until('the third phone card', () => n.asked.length === 3)
  n.setHub(false)
  await until('hub disconnected', () => !rt.hubConnected)
  offlineWeb.resolve('allowed-once')
  assert.equal(await offline, 'allowed-once')
  assert.equal(n.all('interaction.cancel').length, 2, 'a disconnected hub cannot receive the withdrawal yet')
  n.setHub(true)
  await until('reconnection withdraws the Web-answered card', () => n.last('interaction.cancel')?.id === n.asked[2].id)
  offlinePhone.resolve({ allow: false })
})

test('both mode races questions and preserves one timed late answer (#10176)', async (t) => {
  const { n, ctl, ev, rt, answered } = await setup(t, { interactions: 'both' })
  ctl.add('session-both-q', {})
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  const agent = { session: { id: 'session-both-q' } }
  const q = { id: 'q1', question: 'Which db?', options: [{ label: 'pg' }, { label: 'sqlite' }] }

  const phone = deferred()
  const web = deferred()
  const req = { agent, questions: [q] }
  let webSignal
  n.resolve = () => phone.promise
  const pending = ev.waterfall('user-questions/request', req, () => { webSignal = req.signal; return web.promise })
  await until('the first question card', () => n.asked.length === 1)
  phone.resolve({ answers: { 'Which db?': 'pg' } })
  assert.deepEqual(await pending, { answers: [{ id: 'q1', selected: ['pg'] }] })
  assert.equal(webSignal.aborted, true)
  web.resolve({ answers: [{ id: 'q1', selected: ['sqlite'] }] })
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(answered.length, 0, 'a resolved question has no late answer')

  const webPhone = deferred()
  const webAnswer = deferred()
  n.resolve = () => webPhone.promise
  const fromWeb = ev.waterfall('user-questions/request', { agent, questions: [q] }, () => webAnswer.promise)
  await until('the second question card', () => n.asked.length === 2)
  webAnswer.resolve({ answers: [{ id: 'q1', selected: ['sqlite'] }] })
  assert.deepEqual(await fromWeb, { answers: [{ id: 'q1', selected: ['sqlite'] }] })
  await until('the Web answer withdraws the question card', () => n.last('interaction.cancel')?.id === n.asked[1].id)
  webPhone.resolve({ answers: { 'Which db?': 'pg' } })
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(answered.length, 0, 'a late phone answer after Web wins is ignored')

  const latePhone = deferred()
  const timed = new AbortController()
  const timedReq = { agent, questions: [q], signal: timed.signal, wait: { callId: 'call_both_q', timed: true } }
  let timedWebSignal
  n.resolve = () => latePhone.promise
  const timedResult = ev.waterfall('user-questions/request', timedReq, () => {
    timedWebSignal = timedReq.signal
    return new Promise((resolve, reject) => timedReq.signal.addEventListener('abort', () => reject(timedReq.signal.reason), { once: true }))
  })
  await until('the timed question card', () => n.asked.length === 3)
  timed.abort(new Error('ASK_TIMED_OUT'))
  await assert.rejects(timedResult, /ASK_TIMED_OUT/)
  assert.equal(timedWebSignal.aborted, true)
  latePhone.resolve({ answers: { 'Which db?': 'pg' } })
  await until('one timed late answer', () => answered.length === 1)
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(answered, [{ callId: 'call_both_q', answer: { answers: [{ id: 'q1', selected: ['pg'] }] } }])

  const cancelledPhone = deferred()
  const cancelledTimed = new AbortController()
  const cancelledReq = { agent, questions: [q], signal: cancelledTimed.signal, wait: { callId: 'call_cancel_q', timed: true } }
  n.resolve = () => cancelledPhone.promise
  const cancelledResult = ev.waterfall('user-questions/request', cancelledReq, () => {
    return new Promise((resolve, reject) => cancelledReq.signal.addEventListener('abort', () => reject(cancelledReq.signal.reason), { once: true }))
  })
  await until('the externally cancelled timed card', () => n.asked.length === 4)
  cancelledTimed.abort(new Error('question completed outside WebUI'))
  await assert.rejects(cancelledResult, /outside WebUI/)
  await until('external cancellation withdraws the timed card', () => n.last('interaction.cancel')?.id === n.asked[3].id)
  cancelledPhone.resolve({ answers: { 'Which db?': 'sqlite' } })
  await new Promise((resolve) => setImmediate(resolve))
  assert.equal(answered.length, 1, 'an outer responder winning is not a timeout and accepts no late answer')
})

test('reconnects when the node goes away and comes back', async (t) => {
  const { n, ctl, rt } = await setup(t)
  ctl.add('session-r', {})
  rt.start()
  await n.ready
  await until('connected', () => rt.connected)
  await until('first list', () => n.all('runtime.sessions').length === 1)
  n.peer.close(new Error('node restarted'))
  await until('reconnected', () => rt.connected && n.all('runtime.sessions').length >= 2, 5000)
})

/** A workspaceRegistry: its archive set and the archive calls. */
function workspace(archived = []) {
  const w = {
    archivedSessionIds: [...archived],
    calls: [],
    async archiveSession(id, o) {
      w.calls.push({ id, ...o })
      w.archivedSessionIds = [...w.archivedSessionIds, id]
    },
  }
  return w
}

test('archived sessions are not listed, and closing one from the phone archives it and stops its work', async (t) => {
  const ws = workspace(['session-gone'])
  const { n, ctl, rt } = await setup(t, { workspace: ws })
  ctl.add('session-gone', { cwd: '/w/app' })
  ctl.add('session-live', { cwd: '/w/app', running: true })
  rt.start()
  await n.ready
  assert.equal(n.hello.caps.close, true)
  await until('listed', () => n.last('runtime.sessions'))
  assert.deepEqual(n.last('runtime.sessions').sessions.map((s) => s.session_id), ['session-live'])
  await n.peer.call('session.close', { session_id: 'session-live' })
  assert.deepEqual(ws.calls, [{ id: 'session-live', stopActivity: true }])
  await until('gone from the list', () => n.last('runtime.sessions')?.sessions.length === 0)
})

test('another runtime id, name and launcher: in hello, the cards\' ids and the rows\' label', async (t) => {
  const { n, ctl, ev, rt } = await setup(t, { runtime: 'dsh-work', name: 'DSH (work)', launcher: 'dsh-work', interactions: 'phone' })
  ctl.add('session-w', { cwd: '/w/app' })
  rt.start()
  await n.ready
  assert.equal(n.hello.runtime, 'dsh-work')
  assert.equal(n.hello.name, 'DSH (work)')
  assert.equal(n.hello.launcher, 'dsh-work')
  assert.equal(n.hello.agent, 'dsh', 'it still runs DSH\'s agent')
  await until('listed', () => n.last('runtime.sessions'))
  assert.equal(n.last('runtime.sessions').sessions[0].agent_label, 'DSH (work)')
  n.resolve = () => ({ allow: true })
  const outcome = await ev.waterfall('approval/request', { agent: { session: { id: 'session-w' } }, toolName: 'bash', callId: 'c1' }, () => 'unavailable')
  assert.equal(outcome, 'allowed-once')
  assert.ok(n.asked[0].id.includes('dsh-work'), n.asked[0].id)
})

test('the hub\'s tools become DSH tools; a call goes through the node with the calling session', async (t) => {
  const registered = new Map()
  const registerTool = (def) => {
    registered.set(def.name, def)
    return () => registered.delete(def.name)
  }
  const { n, rt } = await setup(t, { registerTool, tools: [
    { name: 'phone_calendar_events', description: 'Calendar', input_schema: { type: 'object', properties: { day: { type: 'string' } } } },
    { name: 'mail_search', description: 'Mail', input_schema: { type: 'object' } },
  ] })
  rt.start()
  await n.ready
  await until('registered', () => registered.size === 2 * mcpToolNames('example').length)
  const def = registered.get('mcp__persona__phone_calendar_events')
  assert.equal(def.description, 'Calendar')
  assert.deepEqual(def.parameters.properties.day, { type: 'string' })
  const value = await def.execute({ day: 'today' }, { agent: { session: { id: 'session-c' } }, signal: AbortSignal.timeout(5000) })
  assert.deepEqual(n.toolCalls.at(-1), { name: 'phone_calendar_events', args: { day: 'today' }, session_id: 'session-c' })
  assert.deepEqual(def.output.render({}, value), [{ type: 'text', text: 'phone_calendar_events ran' }])
  n.callTool = () => ({ content: [{ type: 'text', text: 'iPhone 不在线，请打开 Persona' }], is_error: true })
  await assert.rejects(def.execute({}, { agent: { session: { id: 'session-c' } } }), /iPhone 不在线/)

  // The same tools: nothing re-registered; a changed list replaces them; the hub away keeps them.
  await rt.refreshTools()
  assert.equal(registered.size, 2 * mcpToolNames('example').length)
  n.tools = [{ name: 'phone_reminders', description: 'Reminders', input_schema: { type: 'object' } }]
  await rt.refreshTools()
  assert.deepEqual([...registered.keys()], mcpToolNames('phone_reminders'))
  n.setHub(false)
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(registered.size, mcpToolNames('example').length)
  rt.stop()
  assert.equal(registered.size, 0, 'stopping takes them off')
})

test('the assistant\'s conversations: hello says so, the snapshot goes first, the mode sets DSH\'s preset', async (t) => {
  const set = []
  const agents = { get: (id) => ({ session: { id } }) }
  const permissionPresets = { set: (session, name) => set.push([session.id, name]) }
  const { n, ctl, rt } = await setup(t, { assistant: true, agents, permissionPresets, liveClient: true })
  rt.start()
  await n.ready
  assert.equal(n.hello.caps.assistant, true)
  const { session } = await n.peer.call('session.start', { cwd: '/home/u/.persona/assistant', assistant: true })
  const id = session.session_id
  await n.peer.call('session.send', { session_id: id, text: 'hi', assistant: true, context: '<persona-context>\n- 现在：x\n</persona-context>', mode: 'plan', memory_dir: '/m' })
  assert.deepEqual(ctl.prompts.at(-1).content.map((b) => b.text), ['<persona-context>\n- 现在：x\n</persona-context>', 'hi'])
  assert.deepEqual(set, [[id, 'read-only']])
  await n.peer.call('session.send', { session_id: id, text: 'again', assistant: true, mode: 'plan' })
  assert.equal(set.length, 1, 'the same preset is not set twice')
  await n.peer.call('session.send', { session_id: id, text: 'go', assistant: true, mode: 'bypassPermissions' })
  assert.deepEqual(set.at(-1), [id, 'danger-full-access'])
  // Without assistant the context is not used (another caller's message).
  await n.peer.call('session.send', { session_id: 'session-x', text: 'plain', context: 'ignored' })
  assert.deepEqual(ctl.prompts.at(-1).content.map((b) => b.text), ['plain'])
})

test('assistant presets are active before prompt admission in both directions, without duplicate Plan sets (#8168)', async (t) => {
  const id = 'assistant-modes'
  const session = { id }
  const agents = { get: (sessionId) => sessionId === id ? { session } : undefined }
  const events = []
  let activePreset = 'workspace-write'
  const permissionPresets = { set: (target, name) => {
    assert.equal(target, session)
    activePreset = name
    events.push(`set:${name}`)
  } }
  const { n, ctl, ev, rt } = await setup(t, { assistant: true, agents, permissionPresets })
  const prompt = ctl.prompt.bind(ctl)
  ctl.prompt = async (request) => {
    events.push(`prompt:${request.content.at(-1).text}:${activePreset}`)
    return prompt(request)
  }
  rt.start()
  await n.ready

  const send = (text, mode, client_msg_id) => n.peer.call('session.send', { session_id: id, text, assistant: true, mode, client_msg_id })
  await send('full access', 'bypassPermissions', 'mode-1')
  ev.emit('session/event', { id }, { type: 'turn/start', seq: 1, data: { turn: 1 } })
  await send('inspect only', 'plan', 'mode-2')
  await send('inspect only', 'plan', 'mode-2') // a retry shares the first send's answer
  await send('still inspect', 'plan', 'mode-3')
  await send('full access again', 'bypassPermissions', 'mode-4')

  assert.deepEqual(events, [
    'set:danger-full-access',
    'prompt:full access:danger-full-access',
    'set:read-only',
    'prompt:inspect only:read-only',
    'prompt:still inspect:read-only',
    'set:danger-full-access',
    'prompt:full access again:danger-full-access',
  ])
  assert.deepEqual(ctl.prompts.map((request) => request.requestId), ['mode-1', 'mode-2', 'mode-3', 'mode-4'])
  assert.equal(ctl.prompts[0].mode, 'queue')
  assert.ok(ctl.prompts.slice(1).every((request) => request.mode === 'steer'), 'a running session still steers every later prompt')
})

test('concurrent warm Plan and bypass sends admit each prompt under its own preset (#8168)', async (t) => {
  const id = 'assistant-concurrent-warm'
  const session = { id }
  const agents = { get: (sessionId) => sessionId === id ? { session } : undefined }
  const events = []
  let activePreset = 'workspace-write'
  const permissionPresets = { set: (target, name) => {
    assert.equal(target, session)
    activePreset = name
    events.push(`set:${name}`)
  } }
  const { n, ctl, ev, rt } = await setup(t, { assistant: true, agents, permissionPresets })
  const prompt = ctl.prompt.bind(ctl)
  ctl.prompt = async (request) => {
    events.push(`prompt:${request.content.at(-1).text}:${activePreset}`)
    return prompt(request)
  }
  rt.start()
  await n.ready
  ev.emit('session/event', { id }, { type: 'turn/start', seq: 1, data: { turn: 1 } })

  const send = (text, mode, client_msg_id) => n.peer.call('session.send', { session_id: id, text, assistant: true, mode, client_msg_id })
  await Promise.all([
    send('plan', 'plan', 'warm-plan'),
    send('bypass', 'bypassPermissions', 'warm-bypass'),
  ])

  assert.deepEqual(events, [
    'set:read-only',
    'prompt:plan:read-only',
    'set:danger-full-access',
    'prompt:bypass:danger-full-access',
  ])
  assert.deepEqual(ctl.prompts.map((request) => [request.requestId, request.mode]), [
    ['warm-plan', 'steer'],
    ['warm-bypass', 'steer'],
  ])
})

test('concurrent sends sharing a cold resume keep Plan read-only through prompt admission (#8168)', async (t) => {
  const id = 'assistant-concurrent-cold'
  const live = new Map()
  const agents = { get: (sessionId) => live.get(sessionId) }
  const events = []
  let activePreset = 'danger-full-access'
  let resumeCount = 0
  let releaseResume
  const resume = new Promise((resolve) => { releaseResume = resolve })
  const permissionPresets = { set: (session, name) => {
    activePreset = name
    events.push(`set:${name}`)
  } }
  const { n, ctl, rt } = await setup(t, { assistant: true, agents, permissionPresets })
  ctl.add(id, { cwd: '/home/u/.persona/assistant' })
  ctl.resolveAgent = (sessionId) => {
    resumeCount++
    return resume.then(() => {
      const agent = { session: { id: sessionId } }
      live.set(sessionId, agent)
      return { agent }
    })
  }
  const prompt = ctl.prompt.bind(ctl)
  ctl.prompt = async (request) => {
    events.push(`prompt:${request.content.at(-1).text}:${activePreset}`)
    return prompt(request)
  }
  rt.start()
  await n.ready

  const send = (text, mode, client_msg_id) => n.peer.call('session.send', { session_id: id, text, assistant: true, mode, client_msg_id })
  const pending = Promise.all([
    send('plan', 'plan', 'cold-plan'),
    send('bypass', 'bypassPermissions', 'cold-bypass'),
  ])
  await until('the shared cold resume', () => resumeCount > 0)
  await new Promise((resolve) => setImmediate(resolve))
  releaseResume()
  await pending

  assert.deepEqual(events, [
    'set:read-only',
    'prompt:plan:read-only',
    'set:danger-full-access',
    'prompt:bypass:danger-full-access',
  ])
  assert.equal(resumeCount, 1, 'the later send reuses the Agent resumed by the first')
  assert.deepEqual(ctl.prompts.map((request) => [request.requestId, request.mode]), [
    ['cold-plan', 'queue'],
    ['cold-bypass', 'queue'],
  ])
})

test('prompt admission is serialized per session, not globally or until a model turn ends (#8168)', async (t) => {
  const ids = ['assistant-queue-a', 'assistant-queue-b']
  const sessions = new Map(ids.map((id) => [id, { id }]))
  const agents = { get: (id) => sessions.has(id) ? { session: sessions.get(id) } : undefined }
  const active = new Map(ids.map((id) => [id, 'workspace-write']))
  const events = []
  const admitted = []
  let releaseFirst
  const firstAdmission = new Promise((resolve) => { releaseFirst = resolve })
  const permissionPresets = { set: (session, name) => {
    active.set(session.id, name)
    events.push(`${session.id}:set:${name}`)
  } }
  const { n, ctl, ev, rt } = await setup(t, { assistant: true, agents, permissionPresets })
  const prompt = ctl.prompt.bind(ctl)
  ctl.prompt = async (request) => {
    admitted.push(request.requestId)
    events.push(`${request.sessionId}:prompt:${request.content.at(-1).text}:${active.get(request.sessionId)}`)
    if (request.requestId === 'queue-a-plan') await firstAdmission
    return prompt(request)
  }
  rt.start()
  await n.ready
  for (const id of ids) ev.emit('session/event', { id }, { type: 'turn/start', seq: 1, data: { turn: 1 } })

  const send = (id, text, mode, client_msg_id) => n.peer.call('session.send', { session_id: id, text, assistant: true, mode, client_msg_id })
  const pending = Promise.all([
    send(ids[0], 'plan', 'plan', 'queue-a-plan'),
    send(ids[0], 'bypass', 'bypassPermissions', 'queue-a-bypass'),
    send(ids[1], 'other plan', 'plan', 'queue-b-plan'),
  ])
  await until('the other session admits while the first admission is pending', () => admitted.includes('queue-b-plan'))
  assert.ok(!admitted.includes('queue-a-bypass'), 'the second send to the same session waits for prompt admission')
  releaseFirst()
  await pending

  assert.deepEqual(admitted, ['queue-a-plan', 'queue-b-plan', 'queue-a-bypass'])
  assert.deepEqual(events.filter((event) => event.startsWith(ids[0])), [
    `${ids[0]}:set:read-only`,
    `${ids[0]}:prompt:plan:read-only`,
    `${ids[0]}:set:danger-full-access`,
    `${ids[0]}:prompt:bypass:danger-full-access`,
  ])
  assert.deepEqual(events.filter((event) => event.startsWith(ids[1])), [
    `${ids[1]}:set:read-only`,
    `${ids[1]}:prompt:other plan:read-only`,
  ])
  assert.ok(ctl.prompts.every((request) => request.mode === 'steer'), 'both turns stay open; only admission is awaited')
})

for (const failure of ['resolution', 'preset']) test(`a queued ${failure} failure rejects only its own send and permits a client id retry (#8168)`, async (t) => {
  const id = `assistant-${failure}-queue-failure`
  const session = { id }
  const live = new Map(failure === 'preset' ? [[id, { session }]] : [])
  const agents = { get: (sessionId) => live.get(sessionId) }
  let shouldFail = true
  let activePreset = 'danger-full-access'
  const admitted = []
  const permissionPresets = { set: (target, name) => {
    if (failure === 'preset' && shouldFail) {
      shouldFail = false
      throw new Error('preset failed')
    }
    activePreset = name
  } }
  const { n, ctl, rt } = await setup(t, { assistant: true, agents, permissionPresets })
  ctl.resolveAgent = async (sessionId) => {
    if (shouldFail) {
      shouldFail = false
      return { error: new Error('resolution failed') }
    }
    const agent = { session: { id: sessionId } }
    live.set(sessionId, agent)
    return { agent }
  }
  const prompt = ctl.prompt.bind(ctl)
  ctl.prompt = async (request) => {
    admitted.push([request.requestId, activePreset])
    return prompt(request)
  }
  rt.start()
  await n.ready

  const send = (text, mode, client_msg_id) => n.peer.call('session.send', { session_id: id, text, assistant: true, mode, client_msg_id })
  const results = await Promise.allSettled([
    send('do not admit', 'plan', `retry-${failure}`),
    send('next prompt', 'bypassPermissions', `after-${failure}`),
  ])
  assert.equal(results[0].status, 'rejected')
  assert.match(results[0].reason.message, new RegExp(`${failure} failed`))
  assert.equal(results[1].status, 'fulfilled')
  assert.deepEqual(admitted, [[`after-${failure}`, 'danger-full-access']])

  await send('retry after the queued failure', 'plan', `retry-${failure}`)
  assert.deepEqual(admitted, [
    [`after-${failure}`, 'danger-full-access'],
    [`retry-${failure}`, 'read-only'],
  ])
})

test('an initial Plan prompt resolves a cold Agent and applies read-only before admission (#8168)', async (t) => {
  const id = 'assistant-cold'
  const live = new Map()
  const agents = { get: (sessionId) => live.get(sessionId) }
  const events = []
  let activePreset = 'danger-full-access'
  const permissionPresets = { set: (session, name) => {
    activePreset = name
    events.push(`set:${name}`)
  } }
  const { n, ctl, rt } = await setup(t, { assistant: true, agents, permissionPresets })
  ctl.add(id, { cwd: '/home/u/.persona/assistant' })
  ctl.resolveAgent = async (sessionId) => {
    events.push('resolve')
    const agent = { session: { id: sessionId } }
    live.set(sessionId, agent)
    return { agent }
  }
  const prompt = ctl.prompt.bind(ctl)
  ctl.prompt = async (request) => {
    events.push(`prompt:${activePreset}`)
    // The real controller resolves a cold Agent while admitting the prompt; that is too late to protect this turn.
    const found = await ctl.resolveAgent(request.sessionId)
    if ('error' in found) throw found.error
    return prompt(request)
  }
  rt.start()
  await n.ready

  await n.peer.call('session.send', { session_id: id, text: 'inspect after restart', assistant: true, mode: 'plan', client_msg_id: 'cold-plan' })

  assert.deepEqual(events.slice(0, 3), ['resolve', 'set:read-only', 'prompt:read-only'])
  assert.equal(activePreset, 'read-only')
  assert.equal(ctl.prompts.length, 1)
  assert.equal(ctl.prompts[0].requestId, 'cold-plan')
  assert.equal(ctl.prompts[0].mode, 'queue')
})

test('a cold Agent resolution failure admits no assistant Plan prompt (#8168)', async (t) => {
  const id = 'assistant-resolution-failure'
  const agents = { get: () => undefined }
  const set = []
  let activePreset = 'danger-full-access'
  const { n, ctl, rt } = await setup(t, { assistant: true, agents, permissionPresets: { set: (...args) => set.push(args) } })
  ctl.resolveAgent = async () => ({ error: new Error('cold resume failed') })
  const prompt = ctl.prompt.bind(ctl)
  const admitted = []
  ctl.prompt = async (request) => {
    admitted.push(activePreset)
    return prompt(request)
  }
  rt.start()
  await n.ready

  await assert.rejects(
    n.peer.call('session.send', { session_id: id, text: 'do not run with full access', assistant: true, mode: 'plan' }),
    /cold resume failed/,
  )
  assert.deepEqual(admitted, [])
  assert.equal(activePreset, 'danger-full-access')
  assert.equal(ctl.prompts.length, 0)
  assert.equal(set.length, 0)
})

test('a preset setter failure admits no assistant Plan prompt under the previous full access (#8168)', async (t) => {
  const id = 'assistant-set-failure'
  const session = { id }
  const agents = { get: () => ({ session }) }
  let activePreset = 'danger-full-access'
  const permissionPresets = { set: () => { throw new Error('read-only is unavailable') } }
  const { n, ctl, rt } = await setup(t, { assistant: true, agents, permissionPresets })
  const prompt = ctl.prompt.bind(ctl)
  const admitted = []
  ctl.prompt = async (request) => {
    admitted.push(activePreset)
    return prompt(request)
  }
  rt.start()
  await n.ready

  await assert.rejects(
    n.peer.call('session.send', { session_id: id, text: 'do not run with full access', assistant: true, mode: 'plan' }),
    /read-only is unavailable/,
  )
  assert.deepEqual(admitted, [])
  assert.equal(activePreset, 'danger-full-access')
  assert.equal(ctl.prompts.length, 0)
})

test('the assistant\'s approvals go to the phone even with the Web GUI open; its memory writes are not asked', async (t) => {
  const { n, ctl, ev, rt } = await setup(t, { assistant: true, liveClient: true })
  ctl.add('session-asst', { cwd: '/home/u/.persona/assistant' })
  ctl.add('session-web', { cwd: '/w/app' })
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.length === 2)
  await n.peer.call('session.send', { session_id: 'session-asst', text: 'x', assistant: true, memory_dir: '/m/memory', mode: 'default' })
  n.resolve = () => ({ allow: true })
  const ask = (sid, callId) => ev.waterfall('approval/request', { agent: { session: { id: sid } }, toolName: 'bash', callId }, () => 'web-gui')
  assert.equal(await ask('session-web', 'c0'), 'web-gui', 'a browser is open: the Web GUI asks for its own sessions')
  assert.equal(await ask('session-asst', 'c1'), 'allowed-once')
  assert.equal(n.asked.length, 1)
  ev.emit('session/event', { id: 'session-asst' }, { type: 'assistant/message', seq: 5, data: { message: { content: [
    { type: 'tool-call', id: 'w1', name: 'write', arguments: JSON.stringify({ file_path: '/m/memory/tea.md', content: 'x' }) },
    { type: 'tool-call', id: 'w2', name: 'write', arguments: JSON.stringify({ file_path: '/m/other.md', content: 'x' }) },
    { type: 'tool-call', id: 'w3', name: 'write', arguments: JSON.stringify({ file_path: 'SOUL.md', content: 'x' }) },
    { type: 'tool-call', id: 'w4', name: 'edit', arguments: JSON.stringify({ file_path: 'sub/evil.sh', old_string: 'a', new_string: 'b' }) },
    { type: 'tool-call', id: 'w5', name: 'write', arguments: JSON.stringify({ file_path: '/m/memory/..notes.md', content: 'x' }) },
    { type: 'tool-call', id: 'w6', name: 'write', arguments: JSON.stringify({ file_path: '/m/memory', content: 'x' }) },
    { type: 'tool-call', id: 'w7', name: 'write', arguments: JSON.stringify({ file_path: '/m/memory/bad\0.md', content: 'x' }) },
  ] } } })
  assert.equal(await ask('session-asst', 'w1'), 'allowed-once')
  assert.equal(n.asked.length, 1, 'a memory write is not asked')
  assert.equal(await ask('session-asst', 'w2'), 'allowed-once')
  assert.equal(n.asked.length, 2, 'a write elsewhere is')
  assert.equal(await ask('session-asst', 'w3'), 'allowed-once')
  assert.equal(n.asked.length, 3, 'a relative write resolves against session cwd, not memoryDir')
  assert.equal(await ask('session-asst', 'w4'), 'allowed-once')
  assert.equal(n.asked.length, 4, 'a relative nested edit resolves against session cwd, not memoryDir')
  assert.equal(await ask('session-asst', 'w5'), 'allowed-once')
  assert.equal(n.asked.length, 4, 'a dot-prefixed file inside memoryDir (..notes.md) stays auto-approved')
  assert.equal(await ask('session-asst', 'w6'), 'allowed-once')
  assert.equal(n.asked.length, 5, 'targeting memoryDir itself must ask')
  assert.equal(await ask('session-asst', 'w7'), 'allowed-once')
  assert.equal(n.asked.length, 6, 'NUL byte path must fail closed and ask')
})

test('assistant memory write check resolves realpaths, blocks symlink and dangling-symlink escapes, and requires absolute cwd for relative paths', async (t) => {
  const { mkdtempSync, mkdirSync, symlinkSync, rmSync } = await import('node:fs')
  const { tmpdir } = await import('node:os')
  const { join, sep } = await import('node:path')
  const root = mkdtempSync(join(tmpdir(), 'persona-dsh-mem-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const work = join(root, 'assistant')
  const mem = join(root, 'mem')
  const outside = join(root, 'outside')
  const outsideDeep = join(outside, 'deep')
  mkdirSync(work, { recursive: true })
  mkdirSync(join(mem, 'sub'), { recursive: true })
  mkdirSync(outsideDeep, { recursive: true })
  symlinkSync(outside, join(mem, 'escape'))
  symlinkSync(outsideDeep, join(mem, 'sub', 'deep-escape'))
  symlinkSync(join(outside, 'new-target.md'), join(mem, 'dangling.md'))
  symlinkSync(join(outside, 'missing-dir'), join(mem, 'dangling-dir'))
  symlinkSync(join(mem, 'loop-b'), join(mem, 'loop-a'))
  symlinkSync(join(mem, 'loop-a'), join(mem, 'loop-b'))
  const memAlias = join(root, 'mem-alias')
  symlinkSync(mem, memAlias)

  const { n, ctl, ev, rt } = await setup(t, { assistant: true, liveClient: true })
  ctl.add('session-sym', { cwd: work })
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.length === 1)
  await n.peer.call('session.send', { session_id: 'session-sym', text: 'x', assistant: true, memory_dir: memAlias, mode: 'default' })
  n.resolve = () => ({ allow: true })
  const ask = (callId, agent = { session: { id: 'session-sym', header: { cwd: work } } }) =>
    ev.waterfall('approval/request', { agent, toolName: 'write', callId }, () => 'web-gui')

  ev.emit('session/event', { id: 'session-sym' }, { type: 'assistant/message', seq: 1, data: { message: { content: [
    { type: 'tool-call', id: 'ok-abs', name: 'write', arguments: JSON.stringify({ file_path: join(mem, 'note.md') }) },
    { type: 'tool-call', id: 'ok-rel', name: 'write', arguments: JSON.stringify({ file_path: '../mem/rel-note.md' }) },
    { type: 'tool-call', id: 'bad-rel', name: 'write', arguments: JSON.stringify({ file_path: 'AGENTS.md' }) },
    { type: 'tool-call', id: 'bad-dir-sym', name: 'write', arguments: JSON.stringify({ file_path: join(memAlias, 'escape', 'pwn.md') }) },
    { type: 'tool-call', id: 'bad-sym-dotdot', name: 'write', arguments: JSON.stringify({ file_path: `${join(memAlias, 'sub', 'deep-escape')}/../pwn.md` }) },
    { type: 'tool-call', id: 'bad-dangling-file', name: 'write', arguments: JSON.stringify({ file_path: join(memAlias, 'dangling.md') }) },
    { type: 'tool-call', id: 'bad-dangling-dir', name: 'write', arguments: JSON.stringify({ file_path: join(memAlias, 'dangling-dir', 'sub', 'pwn.md') }) },
    { type: 'tool-call', id: 'bad-loop', name: 'write', arguments: JSON.stringify({ file_path: join(memAlias, 'loop-a') }) },
  ] } } })

  assert.equal(await ask('ok-abs'), 'allowed-once')
  assert.equal(n.asked.length, 0, 'write via canonical realpath matching symlinked memory_dir is auto-approved')
  assert.equal(await ask('ok-rel'), 'allowed-once')
  assert.equal(n.asked.length, 0, 'relative path from cwd that resolves strictly inside memory_dir is auto-approved')
  assert.equal(await ask('bad-rel'), 'allowed-once')
  assert.equal(n.asked.length, 1, 'relative path inside assistant cwd must ask')
  assert.equal(await ask('bad-dir-sym'), 'allowed-once')
  assert.equal(n.asked.length, 2, 'symlinked directory escaping memory_dir must ask')
  assert.equal(await ask('bad-sym-dotdot'), 'allowed-once')
  assert.equal(n.asked.length, 3, 'symlink followed by .. must resolve symlink before .. like the OS kernel and ask')
  assert.equal(await ask('bad-dangling-file'), 'allowed-once')
  assert.equal(n.asked.length, 4, 'dangling symlink file pointing outside memory_dir must ask')
  assert.equal(await ask('bad-dangling-dir'), 'allowed-once')
  assert.equal(n.asked.length, 5, 'dangling symlink directory pointing outside memory_dir must ask')
  assert.equal(await ask('bad-loop'), 'allowed-once')
  assert.equal(n.asked.length, 6, 'symlink loop must fail closed and ask')

  // Unknown session cwd: relative path must fail closed even if ../mem/... would have matched with cwd.
  await n.peer.call('session.send', { session_id: 'session-nocwd', text: 'x', assistant: true, memory_dir: mem, mode: 'default' })
  ev.emit('session/event', { id: 'session-nocwd' }, { type: 'assistant/message', seq: 1, data: { message: { content: [
    { type: 'tool-call', id: 'nocwd-rel', name: 'write', arguments: JSON.stringify({ file_path: 'note.md' }) },
  ] } } })
  assert.equal(await ev.waterfall('approval/request', { agent: { session: { id: 'session-nocwd' } }, toolName: 'write', callId: 'nocwd-rel' }, () => 'web-gui'), 'allowed-once')
  assert.equal(n.asked.length, 7, 'relative write without known absolute cwd must fail closed and ask')

  // Advanced security bypass coverage: clipInput truncation (>24 KiB & multi-field 256B share),
  // POSIX backslash separator, internal symlink + .. namei-vs-resolve split, symlinked cwd,
  // tilde expansion, relative symlink target, stored a.cwd without header.cwd, invalid explicit cwd,
  // prefix collision, trailing slashes, plan mode, memory_dir='/', invalid JSON/non-string file_path,
  // and first write before memoryDir exists on disk.
  const nestedDeep = join(mem, 'a', 'b')
  mkdirSync(nestedDeep, { recursive: true })
  symlinkSync(nestedDeep, join(mem, 'deep-in'))
  symlinkSync('../outside', join(mem, 'rel-escape'))
  const mem2 = join(root, 'mem2')
  mkdirSync(mem2, { recursive: true })
  const nestedWork = join(root, 'nested', 'work')
  const nestedMem = join(root, 'nested', 'mem')
  mkdirSync(nestedWork, { recursive: true })
  mkdirSync(nestedMem, { recursive: true })
  const workLink = join(root, 'work-link')
  symlinkSync(nestedWork, workLink)
  const futureMem = join(root, 'future-mem')

  const paddedEscape2Field = `${mem}/${'./'.repeat(6300)}../outside/SOUL.md`
  const paddedEscapeMany = `${mem}/${'./'.repeat(180)}../outside/SOUL.md`
  const manyFields = { file_path: paddedEscapeMany, content: 'x'.repeat(200) }
  for (let i = 0; i < 120; i++) manyFields[`k${i}`] = 'v'.repeat(300)

  ev.emit('session/event', { id: 'session-sym' }, { type: 'assistant/message', seq: 2, data: { message: { content: [
    { type: 'tool-call', id: 'clip-2field', name: 'write', arguments: JSON.stringify({ file_path: paddedEscape2Field, content: 'x'.repeat(26_000) }) },
    { type: 'tool-call', id: 'clip-manyfield', name: 'write', arguments: JSON.stringify(manyFields) },
    { type: 'tool-call', id: 'bs-escape', name: 'write', arguments: JSON.stringify({ file_path: `${outside}/pwn\\..\\..\\mem\\x.md` }) },
    { type: 'tool-call', id: 'internal-sym-dotdot', name: 'write', arguments: JSON.stringify({ file_path: `${join(mem, 'deep-in')}/../../SOUL.md` }) },
    { type: 'tool-call', id: 'rel-sym-escape', name: 'write', arguments: JSON.stringify({ file_path: join(mem, 'rel-escape', 'pwn.md') }) },
    { type: 'tool-call', id: 'tilde-escape', name: 'write', arguments: JSON.stringify({ file_path: '~/../mem/pwn.md' }) },
    { type: 'tool-call', id: 'tilde-only', name: 'write', arguments: JSON.stringify({ file_path: '~' }) },
    { type: 'tool-call', id: 'prefix-collision', name: 'write', arguments: JSON.stringify({ file_path: join(mem2, 'evil.md') }) },
    { type: 'tool-call', id: 'trailing-dir', name: 'write', arguments: JSON.stringify({ file_path: `${mem}/` }) },
    { type: 'tool-call', id: 'trailing-sub', name: 'write', arguments: JSON.stringify({ file_path: `${mem}/sub/` }) },
    { type: 'tool-call', id: 'bad-json', name: 'write', arguments: '{not-valid-json' },
    { type: 'tool-call', id: 'non-string-path', name: 'write', arguments: JSON.stringify({ file_path: [join(mem, 'note.md')] }) },
    { type: 'tool-call', id: 'stored-cwd-ok', name: 'write', arguments: JSON.stringify({ file_path: '../mem/via-stored-cwd.md' }) },
    { type: 'tool-call', id: 'stored-cwd-bad', name: 'write', arguments: JSON.stringify({ file_path: 'CLAUDE.md' }) },
    { type: 'tool-call', id: 'symlink-cwd-split', name: 'write', arguments: JSON.stringify({ file_path: '../mem/split.md' }) },
  ] } } })

  let expectedAsked = 7
  const mustAskCases = [
    ['clip-2field', '>12 KiB padded file_path (>24 KiB args) must not bypass via clipInput truncation'],
    ['clip-manyfield', 'multi-field 256B share padded file_path must not bypass via clipInput truncation'],
    ...(sep === '/' ? [['bs-escape', 'POSIX backslash path must not split on \\ to escape into memoryDir']] : []),
    ['internal-sym-dotdot', 'internal symlink + .. where lexical resolve escapes memoryDir must ask'],
    ['rel-sym-escape', 'relative symlink target escaping memoryDir must ask'],
    ['tilde-escape', '~/ path must fail closed and ask'],
    ['tilde-only', '~ path must fail closed and ask'],
    ['prefix-collision', 'sibling prefix dir (mem2 vs mem) must ask'],
    ['trailing-dir', 'memoryDir with trailing slash must ask'],
    ['bad-json', 'malformed JSON arguments must fail closed and ask'],
    ['non-string-path', 'non-string file_path must fail closed and ask'],
    ['stored-cwd-bad', 'relative write using stored a.cwd (no header.cwd) outside memoryDir must ask'],
  ]
  for (const [id, msg] of mustAskCases) {
    const agent = id === 'stored-cwd-bad' ? { session: { id: 'session-sym' } } : undefined
    assert.equal(await ask(id, agent), 'allowed-once')
    assert.equal(n.asked.length, ++expectedAsked, msg)
  }

  // Trailing slash on subpath inside memoryDir & stored a.cwd resolving inside memoryDir stay auto-approved.
  assert.equal(await ask('trailing-sub'), 'allowed-once')
  assert.equal(n.asked.length, expectedAsked, 'subpath with trailing slash inside memoryDir is auto-approved')
  assert.equal(await ask('stored-cwd-ok', { session: { id: 'session-sym' } }), 'allowed-once')
  assert.equal(n.asked.length, expectedAsked, 'relative write via stored a.cwd (without header.cwd) inside memoryDir is auto-approved')

  // Explicit invalid (relative) header.cwd must fail closed instead of falling back to valid a.cwd.
  assert.equal(await ask('stored-cwd-ok', { session: { id: 'session-sym', header: { cwd: 'relative/cwd' } } }), 'allowed-once')
  assert.equal(n.asked.length, ++expectedAsked, 'explicit non-absolute cwd must fail closed rather than falling back to a.cwd')

  // Symlinked cwd where namei (nested/work -> nested/mem) and lexical resolve (work-link -> root/mem) disagree must fail closed!
  assert.equal(await ask('symlink-cwd-split', { session: { id: 'session-sym', header: { cwd: workLink } } }), 'allowed-once')
  assert.equal(n.asked.length, ++expectedAsked, 'symlinked cwd causing namei-vs-resolve split must fail closed and ask')

  // mode: 'plan' blocks even a valid memory write.
  await n.peer.call('session.send', { session_id: 'session-sym', text: 'plan', assistant: true, memory_dir: memAlias, mode: 'plan' })
  assert.equal(await ask('ok-abs'), 'allowed-once')
  assert.equal(n.asked.length, ++expectedAsked, 'plan mode must ask even for valid memoryDir writes')

  // memory_dir: '/' fails closed.
  await n.peer.call('session.send', { session_id: 'session-rootmem', text: 'x', assistant: true, memory_dir: '/', mode: 'default' })
  ev.emit('session/event', { id: 'session-rootmem' }, { type: 'assistant/message', seq: 1, data: { message: { content: [
    { type: 'tool-call', id: 'rootmem-w', name: 'write', arguments: JSON.stringify({ file_path: join(outside, 'x.md') }) },
  ] } } })
  assert.equal(await ev.waterfall('approval/request', { agent: { session: { id: 'session-rootmem' } }, toolName: 'write', callId: 'rootmem-w' }, () => 'web-gui'), 'allowed-once')
  assert.equal(n.asked.length, ++expectedAsked, 'memory_dir=/ must fail closed and ask')

  // First write before memoryDir exists on disk succeeds; escaping it still asks.
  await n.peer.call('session.send', { session_id: 'session-future', text: 'x', assistant: true, memory_dir: futureMem, mode: 'default' })
  ev.emit('session/event', { id: 'session-future' }, { type: 'assistant/message', seq: 1, data: { message: { content: [
    { type: 'tool-call', id: 'future-ok', name: 'write', arguments: JSON.stringify({ file_path: join(futureMem, 'MEMORY.md') }) },
    { type: 'tool-call', id: 'future-esc', name: 'write', arguments: JSON.stringify({ file_path: `${futureMem}/../evil.md` }) },
  ] } } })
  assert.equal(await ev.waterfall('approval/request', { agent: { session: { id: 'session-future' } }, toolName: 'write', callId: 'future-ok' }, () => 'web-gui'), 'allowed-once')
  assert.equal(n.asked.length, expectedAsked, 'first write before memoryDir exists on disk is auto-approved')
  assert.equal(await ev.waterfall('approval/request', { agent: { session: { id: 'session-future' } }, toolName: 'write', callId: 'future-esc' }, () => 'web-gui'), 'allowed-once')
  assert.equal(n.asked.length, ++expectedAsked, 'escaping non-existent memoryDir via .. must ask')
})

test('a gated command in an assistant conversation\'s shell is stopped; elsewhere it runs as before', async (t) => {
  const gated = { name: 'example_api', description: 'Example', input_schema: { type: 'object' }, shell: ['ex', 'example/scripts/ex.sh'] }
  const { n, ctl, ev, rt } = await setup(t, { assistant: true, registerTool: () => () => {}, tools: [gated] })
  ctl.add('session-asst', { cwd: '/home/u/.persona/assistant' })
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  await rt.refreshTools()
  const pre = (sid, command) => ev.waterfall('tools/pre-execute', { name: 'bash', arguments: { command }, agent: { session: { id: sid } } }, () => ({ kind: 'allow' }))
  const denied = await pre('session-asst', 'ex GET /me')
  assert.equal(denied.kind, 'deny')
  assert.match(denied.reason, /example_api/)
  assert.equal((await pre('session-asst', 'bash ~/.persona/skills/example/scripts/ex.sh POST /x')).kind, 'deny')
  assert.equal((await pre('session-asst', 'echo exit && ls')).kind, 'allow')
  assert.equal((await pre('session-other', 'ex GET /me')).kind, 'allow')
})

test('assistant shell gates block obfuscated shell invocations, subagents, and uncheckable commands', async (t) => {
  const gated = { name: 'example_api', description: 'Example', input_schema: { type: 'object' }, shell: ['ex', 'example/scripts/ex.sh'] }
  const { n, ctl, ev, rt } = await setup(t, { assistant: true, registerTool: () => () => {}, tools: [gated] })
  ctl.add('session-asst', { cwd: '/home/u/.persona/assistant' })
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  await rt.refreshTools()

  const pre = (payload) => ev.waterfall('tools/pre-execute', payload, () => ({ kind: 'allow' }))
  const asstAgent = { session: { id: 'session-asst' } }

  for (const command of [
    '/usr/local/bin/ex GET /me',
    './ex GET /me',
    'ex;echo hi',
    'ex|cat',
    'ex&&id',
    'echo $(ex)',
    'echo `ex`',
    '"ex" GET /me',
    "'ex' GET /me",
    '\\ex GET /me',
    'e""x GET /me',
    "sh -c 'ex GET /me'",
    "bash -c 'e\"\"x GET /me'",
    "eval 'ex GET /me'",
  ]) {
    const res = await pre({ name: 'bash', arguments: { command }, agent: asstAgent })
    assert.equal(res.kind, 'deny', `must deny obfuscated gated command: ${command}`)
    assert.match(res.reason, /example_api/)
  }

  // Tool name casing / shell alias, cmd field, JSON string arguments, and mixed command+cmd.
  assert.equal((await pre({ name: 'shell', arguments: { command: 'ex GET /me' }, agent: asstAgent })).kind, 'deny')
  assert.equal((await pre({ name: 'Bash', arguments: { cmd: 'ex GET /me' }, agent: asstAgent })).kind, 'deny')
  assert.equal((await pre({ name: 'bash', arguments: JSON.stringify({ command: 'ex GET /me' }), agent: asstAgent })).kind, 'deny')
  assert.equal((await pre({ name: 'bash', arguments: { command: 'echo ok', cmd: 'ex GET /me' }, agent: asstAgent })).kind, 'deny')

  // Uncheckable / malformed / empty shell arguments fail closed in assistant sessions.
  for (const args of [{ command: '' }, { command: '   ' }, { command: 123 }, {}, null, 'not-json']) {
    const res = await pre({ name: 'bash', arguments: args, agent: asstAgent })
    assert.equal(res.kind, 'deny', `must fail closed on uncheckable args: ${JSON.stringify(args)}`)
  }

  // Assistant subagent and newly-created assistant-cwd agent (before listSessions) are also gated.
  assert.equal((await pre({ name: 'bash', arguments: { command: 'ex GET /me' }, agent: { session: { id: 'sub-1', header: { parentSession: 'session-asst' } } } })).kind, 'deny')
  assert.equal((await pre({ name: 'bash', arguments: { command: 'ex GET /me' }, agent: { session: { id: 'brand-new', header: { cwd: '/home/u/.persona/assistant' } } } })).kind, 'deny')

  // Safe commands remain allowed for assistant and subagent; gated commands remain allowed for non-assistant.
  assert.equal((await pre({ name: 'bash', arguments: { command: 'echo ok' }, agent: asstAgent })).kind, 'allow')
  assert.equal((await pre({ name: 'bash', arguments: { command: 'echo ok' }, agent: { session: { id: 'sub-1', header: { parentSession: 'session-asst' } } } })).kind, 'allow')
  assert.equal((await pre({ name: 'bash', arguments: { command: 'ex GET /me' }, agent: { session: { id: 'session-other' } } })).kind, 'allow')
})

test('assistant shell gates load without registerTool (phoneTools=false) and fail closed on disconnect, error, or mid-flight drop', async (t) => {
  const gated = { name: 'example_api', description: 'Example', input_schema: { type: 'object' }, shell: ['ex'] }
  // Intentionally omit registerTool (matches config.phoneTools === false in plugin.js).
  const { n, ctl, ev, rt } = await setup(t, { assistant: true, tools: [gated] })
  ctl.add('session-asst', { cwd: '/home/u/.persona/assistant' })
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))

  const pre = (command) => ev.waterfall('tools/pre-execute', { name: 'bash', arguments: { command }, agent: { session: { id: 'session-asst' } } }, () => ({ kind: 'allow' }))

  // Without registerTool, gates still load automatically and enforce rules.
  assert.equal((await pre('ex GET /me')).kind, 'deny')
  assert.equal((await pre('echo ok')).kind, 'allow')

  // Hub disconnect invalidates rules and fails closed even for safe commands.
  n.setHub(false)
  await until('hub down', () => !rt.hubConnected)
  assert.equal((await pre('echo ok')).kind, 'deny')
  assert.equal((await pre('ex GET /me')).kind, 'deny')

  // Hub reconnect with failing mcp.tools stays fail-closed.
  const origReq = n.peer.onRequest
  let failTools = true
  n.peer.onRequest = async (m, p) => {
    if (m === 'mcp.tools' && failTools) throw new Error('hub tools unavailable')
    return origReq(m, p)
  }
  n.setHub(true)
  await until('hub back', () => rt.hubConnected)
  assert.equal((await pre('echo ok')).kind, 'deny')

  // Once mcp.tools recovers, on-demand refresh loads rules and restores normal allow/deny.
  failTools = false
  assert.equal((await pre('ex GET /me')).kind, 'deny')
  assert.equal((await pre('echo ok')).kind, 'allow')

  // Mid-flight disconnect during refresh must not mark stale empty results as ready.
  n.setHub(false)
  await until('hub down 2', () => !rt.hubConnected)
  const gate = deferred()
  let intercepted = false
  n.peer.onRequest = async (m, p) => {
    if (m === 'mcp.tools' && p?.assistant && !intercepted) {
      intercepted = true
      await gate.promise
      return { tools: [] }
    }
    return origReq(m, p)
  }
  n.setHub(true)
  await until('hub back 2', () => rt.hubConnected)
  await until('refresh in flight', () => intercepted)
  // Disconnect while the first assistant tools request is still in flight, then resolve stale [].
  n.setHub(false)
  await until('hub down 3', () => !rt.hubConnected)
  gate.resolve()
  // Flush the ordered socket so the stale [] mcp.tools reply is guaranteed processed before assertions.
  await n.peer.call('launchers.models', {})
  assert.equal((await pre('ex GET /me')).kind, 'deny')
  // Reconnect again: fresh fetch runs and enforces the real gate (not stale []).
  n.setHub(true)
  await until('hub back 3', () => rt.hubConnected)
  assert.equal((await pre('ex GET /me')).kind, 'deny')
  assert.equal((await pre('echo ok')).kind, 'allow')

  // Periodic refresh failure after rules were previously ready clears shellReady and fails closed.
  failTools = true
  n.peer.onRequest = async (m, p) => {
    if (m === 'mcp.tools' && p?.assistant && failTools) throw new Error('periodic refresh failed')
    return origReq(m, p)
  }
  await rt.refreshTools()
  assert.equal((await pre('echo ok')).kind, 'deny')
  failTools = false
  assert.equal((await pre('echo ok')).kind, 'allow')
})

test('assistant shell gates fail closed during runtime.hello and across node reconnects, honor exec.signal, and allow empty gates when hubReady', async (t) => {
  const helloGate = deferred()
  const gated = { name: 'example_api', description: 'Example', input_schema: { type: 'object' }, shell: ['ex'] }
  const { n, ctl, ev, rt } = await setup(t, { assistant: true, tools: [gated] })
  n.hub = false
  n.holdHello = helloGate.promise
  ctl.add('session-asst', { cwd: '/home/u/.persona/assistant' })
  rt.start()
  await until('hello reached node', () => n.hellos === 1)

  const pre = (command, extra = {}) => ev.waterfall(
    'tools/pre-execute',
    { name: 'bash', arguments: { command }, agent: { session: { id: 'session-asst', header: { cwd: '/home/u/.persona/assistant' } } }, ...extra },
    () => ({ kind: 'allow' }),
  )

  // Path A: while runtime.hello is in flight (rt.connected && !rt.ready), must fail closed without touching mcp.tools.
  assert.equal(rt.hubConnected, false)
  assert.equal((await pre('ex GET /me')).kind, 'deny')
  assert.equal((await pre('echo ok')).kind, 'deny')

  // Release hello with hub: false -> still fail closed (must not hit TDZ or latch rejected promise).
  helloGate.resolve()
  await n.ready
  assert.equal(rt.hubConnected, false)
  assert.equal((await pre('ex GET /me')).kind, 'deny')
  // Explicitly calling refreshTools() while !hubReady() must resolve cleanly (no TDZ ReferenceError).
  await rt.refreshTools()

  // Bring hub up with empty tools -> legitimate empty rule set allows safe commands (no false positives).
  n.tools = []
  n.setHub(true)
  await until('hub up with empty tools', () => rt.hubConnected)
  assert.equal((await pre('echo ok')).kind, 'allow')

  // Path B: node socket disconnects (p.done clears peer/readyPeer without onHub(false)) and reconnects with a new gate.
  const reconnectHello = deferred()
  n.holdHello = reconnectHello.promise
  n.tools = [{ name: 'new_api', description: 'New', input_schema: { type: 'object' }, shell: ['newcmd'] }]
  n.peer.close(new Error('node restarted'))
  await until('reconnect hello in flight', () => n.hellos === 2)
  // Stale pre-reconnect shellReady must NOT allow newcmd while runtime.hello is still in flight.
  assert.equal(rt.hubConnected, false)
  assert.equal((await pre('newcmd run')).kind, 'deny')
  reconnectHello.resolve()
  await until('hub ready after reconnect', () => rt.hubConnected)
  assert.equal((await pre('newcmd run')).kind, 'deny')
  assert.equal((await pre('echo ok')).kind, 'allow')

  // Aborting exec.signal while awaiting refreshShellGates returns fail-closed immediately.
  n.setHub(false)
  await until('hub down for abort test', () => !rt.hubConnected)
  const slowTools = deferred()
  const origReq = n.peer.onRequest
  n.peer.onRequest = async (m, p) => {
    if (m === 'mcp.tools' && p?.assistant) {
      await slowTools.promise
      return { tools: n.tools }
    }
    return origReq(m, p)
  }
  n.setHub(true)
  await until('hub up for abort test', () => rt.hubConnected)
  const ac = new AbortController()
  const pending = pre('echo ok', { signal: ac.signal })
  ac.abort(new Error('cancelled'))
  assert.equal((await pending).kind, 'deny')
  slowTools.resolve()
  await until('recovered after slow tools', async () => (await pre('echo ok')).kind === 'allow')
})

test('assistant shell gates decouple from slow registerTool fetches and cover pre-existing multi-hop assistant agents', async (t) => {
  const gated = { name: 'example_api', description: 'Example', input_schema: { type: 'object' }, shell: ['ex'] }
  const preExisting = { session: { id: 'asst-root', header: { cwd: '/home/u/.persona/assistant' } } }
  const midParent = { session: { id: 'sub-mid', header: { parentSession: 'asst-root', cwd: '/tmp/work' } } }
  const agentsMap = new Map([['asst-root', preExisting], ['sub-mid', midParent]])
  const agents = { get: (id) => agentsMap.get(id), list: () => [...agentsMap.values()] }

  const nonAssistantHang = deferred()
  const { n, ev, rt } = await setup(t, {
    assistant: true,
    registerTool: () => () => {},
    tools: [gated],
    agents,
  })
  rt.start()
  await n.ready
  const origReq = n.peer.onRequest
  n.peer.onRequest = async (m, p) => {
    if (m === 'mcp.tools' && !p?.assistant) {
      await nonAssistantHang.promise
      return { tools: [] }
    }
    return origReq(m, p)
  }
  // Force a fresh reconnect refresh where non-assistant mcp.tools hangs indefinitely while assistant mcp.tools succeeds.
  n.setHub(false)
  await until('hub down', () => !rt.hubConnected)
  n.setHub(true)
  await until('hub back', () => rt.hubConnected)

  const pre = (agent, command) => ev.waterfall('tools/pre-execute', { name: 'bash', arguments: { command }, agent }, () => ({ kind: 'allow' }))
  // Pre-existing assistant root (seeded in start() without listSessions) and 2-hop subagent are gated without stalling on non-assistant mcp.tools!
  assert.equal((await pre({ session: { id: 'asst-root' } }, 'ex GET /me')).kind, 'deny')
  assert.equal((await pre({ session: { id: 'sub-leaf', header: { parentSession: 'sub-mid', cwd: '/tmp/work' } } }, 'ex GET /me')).kind, 'deny')
  assert.equal((await pre({ session: { id: 'sub-leaf', header: { parentSession: 'sub-mid', cwd: '/tmp/work' } } }, 'echo ok')).kind, 'allow')
  nonAssistantHang.resolve()
})

test('the assistant\'s own tools are only for its conversations, called as the assistant\'s', async (t) => {
  const registered = new Map()
  const registerTool = (def, opts) => {
    registered.set(def.name, { def, opts })
    return () => registered.delete(def.name)
  }
  const n0 = { tools: [{ name: 'phone_contacts', description: 'Contacts', input_schema: { type: 'object' } }] }
  const { n, ctl, rt } = await setup(t, { assistant: true, registerTool, tools: n0.tools })
  ctl.add('session-asst', { cwd: '/home/u/.persona/assistant' })
  n.tools = n0.tools
  const plainTools = [...n0.tools, { name: 'report_to_supervisor', description: 'Report', input_schema: { type: 'object' } }]
  const withAssistant = [...n0.tools, { name: 'ask_user', description: 'Ask', input_schema: { type: 'object' } }]
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  // The fake node lists the same tools whatever is asked: make it answer as a hub does.
  n.peer.onRequest = ((orig) => async (m, p) => (m === 'mcp.tools' ? { tools: p?.assistant ? withAssistant : plainTools } : orig(m, p)))(n.peer.onRequest)
  await rt.refreshTools()
  await until('registered', () => registered.size === 3 * mcpToolNames('example').length)
  const report = registered.get('mcp__persona__report_to_supervisor')
  assert.equal(report.opts.when({ agent: { session: { id: 'session-asst' } } }), false)
  assert.equal(report.opts.when({ agent: { session: { id: 'session-other' } } }), true)
  await report.def.execute({ kind: 'progress' }, { agent: { session: { id: 'session-other' } } })
  assert.deepEqual(n.toolCalls.at(-1), {
    name: 'report_to_supervisor', args: { kind: 'progress' }, session_id: 'session-other',
  })
  const ask = registered.get('mcp__persona__ask_user')
  assert.equal(typeof ask.opts?.when, 'function')
  assert.equal(ask.opts.when({ agent: { session: { id: 'session-asst' } } }), true)
  assert.equal(ask.opts.when({ agent: { session: { id: 'session-other' } } }), false)
  assert.equal(registered.get('mcp__persona__phone_contacts').opts, undefined, 'the phone\'s tools are for every session')
  await ask.def.execute({}, { agent: { session: { id: 'session-asst' } } })
  assert.deepEqual(n.toolCalls.at(-1), { name: 'ask_user', args: {}, session_id: 'session-asst', assistant: true })
  await registered.get('mcp__persona__phone_contacts').def.execute({}, { agent: { session: { id: 'session-other' } } })
  assert.equal(n.toolCalls.at(-1).assistant, undefined)
})

test('leader tools activate only for the calling leader and invoke the hub as its Agent', async (t) => {
  const registered = new Map()
  const registerTool = (def, opts) => {
    registered.set(def.name, { def, opts })
    return () => registered.delete(def.name)
  }
  const { n, ctl, rt } = await setup(t, { registerTool, features: ['leader_tools'] })
  ctl.add('session-leader')
  ctl.add('session-worker')
  let leader = 'session-leader'
  const plan = { name: 'task_plan', description: 'Plan', input_schema: { type: 'object' } }
  rt.start()
  await n.ready
  const original = n.peer.onRequest
  n.peer.onRequest = async (method, params) => {
    if (method !== 'mcp.tools' || !params.leader_only) return original(method, params)
    return { tools: !params.session_id || params.session_id === leader ? [plan] : [] }
  }
  await rt.refreshTools()
  const registeredPlan = await until('leader tools registered', () => registered.get('mcp__persona__task_plan'))
  const context = id => ({ agent: { session: { id } }, signal: new AbortController().signal })
  const leaderStep = context('session-leader')
  const workerStep = context('session-worker')
  assert.equal(await registeredPlan.opts.when(leaderStep), true)
  assert.equal(await registeredPlan.opts.when(workerStep), false)
  await assert.rejects(registeredPlan.def.execute({}, workerStep), /unavailable to this session/)
  await registeredPlan.def.execute({ tasks: [] }, leaderStep)
  assert.deepEqual(n.toolCalls, [{ name: 'task_plan', args: { tasks: [] }, session_id: 'session-leader' }])
  leader = ''
  assert.equal(await registeredPlan.opts.when(context('session-leader')), false)
})

test('an Agent leader and an assistant keep their own schemas for the same tool name', async (t) => {
  const registry = () => {
    const tools = new Map()
    return {
      tools,
      register(def, opts) {
        tools.set(def.name, { def, opts })
        return () => tools.delete(def.name)
      },
    }
  }
  const globalTools = registry()
  const agentTools = registry()
  const assistantTools = registry()
  const assistantCwd = '/home/u/.persona/assistant'
  const assistant = { session: { id: 'session-assistant', header: { cwd: assistantCwd } }, ctx: { tools: assistantTools } }
  const leader = { session: { id: 'session-leader' }, ctx: { tools: agentTools } }
  const { n, ctl, ev, rt } = await setup(t, {
    assistant: true, registerTool: globalTools.register, features: ['leader_tools'],
    agents: { list: () => [assistant] },
  })
  ctl.add(assistant.session.id, { cwd: assistantCwd })
  ctl.add(leader.session.id)
  rt.start()
  await n.ready
  const original = n.peer.onRequest
  n.peer.onRequest = (method, params) => {
    if (method !== 'mcp.tools') return original(method, params)
    if (params.assistant) return { tools: [{ name: 'task_plan', description: 'Assistant plan' }] }
    if (params.leader_only) return { tools: [{ name: 'task_plan', description: 'Restricted leader plan' }] }
    return { tools: [] }
  }
  await rt.refreshTools()
  const name = 'mcp__persona__task_plan'
  const globalPlan = await until('assistant plan', () => globalTools.tools.get(name))
  ev.emit('agent/created', { agent: leader })
  const scopedPlan = agentTools.tools.get(name)
  assert.equal(assistantTools.tools.size, 0)
  assert.equal(globalPlan.def.description, 'Assistant plan')
  assert.equal(scopedPlan.def.description, 'Restricted leader plan')
  assert.equal(globalPlan.opts.when({ agent: assistant }), true)
  assert.equal(globalPlan.opts.when({ agent: leader }), false)
  assert.equal(await scopedPlan.opts.when({ agent: leader }), true)
  await globalPlan.def.execute({}, { agent: assistant })
  await scopedPlan.def.execute({}, { agent: leader })
  assert.deepEqual(n.toolCalls, [
    { name: 'task_plan', args: {}, session_id: assistant.session.id, assistant: true },
    { name: 'task_plan', args: {}, session_id: leader.session.id },
  ])
  rt.stop()
  assert.equal(agentTools.tools.size, 0)
})

test('advertises DSH configured models as launchers in hello and launchers.models (#5572)', async (t) => {
  const dshModels = [
    {
      provider: 'deepseek',
      name: 'DeepSeek',
      models: [
        { id: 'deepseek-chat', name: 'DeepSeek V3', default: true },
        { id: 'deepseek-reasoner', name: 'DeepSeek R1' },
      ],
    },
    {
      provider: 'anthropic',
      name: 'Anthropic',
      models: [
        { id: 'claude-opus-5-5', name: 'Claude Opus 5.5' },
      ],
    },
  ]
  const { n, rt } = await setup(t, { models: dshModels })
  rt.start()
  await n.ready
  assert.equal(n.hello.caps.models, true)
  assert.ok(Array.isArray(n.hello.launchers))
  const dshLauncher = n.hello.launchers.find((l) => l.name === 'dsh')
  assert.ok(dshLauncher, 'main dsh launcher is advertised')
  assert.equal(dshLauncher.model, 'DeepSeek V3')
  assert.equal(dshLauncher.models.length, 3)

  const anthropicLauncher = n.hello.launchers.find((l) => l.name === 'anthropic')
  assert.ok(anthropicLauncher, 'provider group launcher is advertised')
  assert.equal(anthropicLauncher.models.length, 1)

  const res = await n.peer.call('launchers.models', {})
  assert.equal(res.agent, 'dsh')
  assert.ok(res.models.some((m) => m.id === 'deepseek-chat'))
})

test('reads models from the session controller modelCatalog when nothing else provides them (#6451)', async (t) => {
  const { n, ctl, rt } = await setup(t)
  assert.equal(ctl.listModels, undefined)
  assert.equal(ctl.models, undefined)
  ctl.modelCatalog = async () => ({
    groups: [
      { id: 'deepseek', name: 'DeepSeek', models: [{ id: 'deepseek-chat', name: 'DeepSeek V3' }, { id: 'deepseek-reasoner', name: 'DeepSeek R1' }] },
      { id: 'anthropic', name: 'Anthropic', models: [{ id: 'claude-opus-5-5', name: 'Claude Opus 5.5' }] },
    ],
  })
  rt.start()
  await n.ready
  const res = await n.peer.call('launchers.models', {})
  assert.deepEqual(res.models.map((m) => m.id).sort(), ['claude-opus-5-5', 'deepseek-chat', 'deepseek-reasoner'])
  const names = (await n.peer.call('launchers.models', {})).models.map((m) => m.name)
  assert.ok(names.includes('DeepSeek V3'))
})

test('keeps reading models from controller.listModels when modelCatalog is absent (#6451)', async (t) => {
  const { n, ctl, rt } = await setup(t)
  ctl.listModels = async () => [{ provider: 'deepseek', name: 'DeepSeek', models: [{ id: 'deepseek-chat', name: 'DeepSeek V3' }] }]
  rt.start()
  await n.ready
  const res = await n.peer.call('launchers.models', {})
  assert.deepEqual(res.models.map((m) => m.id), ['deepseek-chat'])
})

test('selects model before sending the first prompt when model is specified (#5572)', async (t) => {
  const dshModels = [
    {
      provider: 'deepseek',
      models: [{ id: 'deepseek-chat', name: 'DeepSeek V3', default: true }],
    },
    {
      provider: 'anthropic',
      models: [{ id: 'claude-opus-5-5', name: 'Claude Opus 5.5' }],
    },
  ]
  const { n, ctl, rt } = await setup(t, { models: dshModels })
  rt.start()
  await n.ready

  const { session } = await n.peer.call('session.start', {
    cwd: '/w/new',
    model: 'claude-opus-5-5',
    prompt: 'hello model',
  })
  assert.equal(session.session_id, 'session-new')
  assert.equal(ctl.selectedModels.length, 1)
  assert.deepEqual(ctl.selectedModels[0], {
    sessionId: 'session-new',
    provider: 'anthropic',
    model: 'claude-opus-5-5',
    at: ctl.selectedModels[0].at,
  })
  assert.equal(ctl.prompts.length, 1)
  assert.equal(ctl.prompts[0].sessionId, 'session-new')

  // Supports provider/model notation
  await n.peer.call('session.start', {
    cwd: '/w/new2',
    model: 'anthropic/claude-opus-5-5',
  })
  assert.equal(ctl.selectedModels.length, 2)
  assert.equal(ctl.selectedModels[1].provider, 'anthropic')
  assert.equal(ctl.selectedModels[1].model, 'claude-opus-5-5')

  // Supports provider group launcher
  await n.peer.call('session.start', {
    cwd: '/w/new3',
    launcher: 'anthropic',
    model: 'claude-opus-5-5',
  })
  assert.equal(ctl.selectedModels.length, 3)
  assert.equal(ctl.selectedModels[2].provider, 'anthropic')
  assert.equal(ctl.selectedModels[2].model, 'claude-opus-5-5')
})

test('refuses session start with a clear error when model is unknown (#5572)', async (t) => {
  const dshModels = [
    {
      provider: 'deepseek',
      models: [{ id: 'deepseek-chat', name: 'DeepSeek V3', default: true }],
    },
  ]
  const { n, ctl, rt } = await setup(t, { models: dshModels })
  rt.start()
  await n.ready

  await assert.rejects(
    n.peer.call('session.start', {
      cwd: '/w/new',
      model: 'nonexistent-model',
      prompt: 'hello',
    }),
    (err) => {
      assert.match(err.message, /nonexistent-model/)
      return true
    }
  )
  assert.equal(ctl.selectedModels.length, 0)
  assert.equal(ctl.prompts.length, 0)
})

// ---- subagents and background work (docs/integrations/dsh.md "Subagents and background tasks") ----

const ev2 = (seq, type, data) => ({ type, seq, time: 1_700_000_000_000 + seq, data })
const toolCall = (seq, callId, name) => ev2(seq, 'tool/call', { callId, name, arguments: '{}' })
const toolResult = (seq, callId, body) => ev2(seq, 'tool/result', { message: { role: 'tool', toolCallId: callId, content: [{ type: 'text', text: body }] } })

/** Emits events on a session as DSH does (its log grows, then session/event). */
function emitOn(ctl, ev, id, events, header) {
  for (const e of events) {
    ctl.sessions.get(id).events.push(e)
    ev.emit('session/event', { id, header }, e)
  }
}

const rowOf = (n, id) => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === id)

test('a session lists the subagents and background jobs it left running, and they finish', async (t) => {
  const { n, ctl, ev, rt } = await setup(t)
  ctl.add('session-p', { cwd: '/w/app' })
  rt.start()
  await n.ready
  await until('listed', () => rowOf(n, 'session-p'))
  emitOn(ctl, ev, 'session-p', [
    ev2(0, 'turn/start', {}),
    toolCall(1, 'call_sub', 'subagent'),
    ev2(2, 'subagent/catalog', { version: 0, childId: 'child-1', childCreatedAt: 1_700_000_000_000, mode: 'continuable', label: 'Map the code' }),
    toolResult(3, 'call_sub', 'started subagent child-1'),
    toolCall(4, 'call_wf', 'workflow'),
    ev2(5, 'job/registered', { jobId: 'workflow-1', owner: 'session-p', kind: 'workflow', label: 'review', startedAt: 1_700_000_000_005, toolCall: { callId: 'call_wf' } }),
    toolResult(6, 'call_wf', 'started workflow workflow-1'),
    toolCall(7, 'call_fg', 'bash'),
    ev2(8, 'job/registered', { jobId: 'bash-1', owner: 'session-p', kind: 'bash', label: 'make test', startedAt: 1_700_000_000_008, toolCall: { callId: 'call_fg' } }),
  ])
  ctl.add('child-1', { cwd: '/w/app', origin: 'subagent', parentSessionId: 'session-p', running: true })
  emitOn(ctl, ev, 'session-p', [ev2(9, 'job/settled', { jobId: 'bash-1', owner: 'session-p', kind: 'bash' }), toolResult(10, 'call_fg', 'ok'), ev2(11, 'turn/end', { kind: 'completed' })])

  await until('the turn done, its subagent and workflow still running', () => {
    const r = rowOf(n, 'session-p')
    return r?.status === 'done' && r.subagents?.length === 1 && r.background?.length === 1
  })
  const row = rowOf(n, 'session-p')
  assert.deepEqual(row.subagents[0], { id: 'child-1', description: 'Map the code', type: 'subagent', running: true, tool_use_id: 'call_sub',
    started_at: new Date(1_700_000_000_000).toISOString(), updated_at: row.subagents[0].updated_at })
  assert.deepEqual(row.background, [{ id: 'workflow-1', kind: 'agent', description: 'review', started_at: new Date(1_700_000_000_005).toISOString() }])
  assert.ok(!n.last('runtime.sessions').sessions.some((s) => s.session_id === 'child-1'), 'a subagent is not a session of its own in the list')

  // The subagent stops and the workflow settles: nothing left running.
  ev.emit('api-session/status', 'child-1', false)
  ctl.sessions.get('child-1').summary.running = false
  emitOn(ctl, ev, 'session-p', [ev2(12, 'job/settled', { jobId: 'workflow-1', owner: 'session-p', kind: 'workflow' })])
  await until('all done', () => {
    const r = rowOf(n, 'session-p')
    return r?.subagents?.[0]?.running === false && r.background === undefined
  })
})

test('a subagent reads, watches and asks as "<parent>@<child>", through its parent', async (t) => {
  const { n, ctl, ev, rt } = await setup(t, { interactions: 'phone' })
  ctl.add('session-p', { cwd: '/w/app' }, [toolResult(0, 'call_sub', 'started subagent child-1')])
  ctl.add('child-1', { cwd: '/w/app', origin: 'subagent', parentSessionId: 'session-p', running: true }, [text(0, 'user', 'map it'), text(1, 'assistant', 'mapping')])
  rt.start()
  await n.ready
  await until('listed', () => rowOf(n, 'session-p')?.subagents?.length === 1)

  const page = await n.peer.call('session.items', { session_id: 'session-p@child-1', turns: 1 })
  assert.deepEqual(page.items.map((i) => i.text), ['map it', 'mapping'])
  assert.equal(page.history, 'dsh:session-p@child-1')
  assert.deepEqual(ctl.pages.at(-1), { kind: 'subagent', parentSessionId: 'session-p', childSessionId: 'child-1', mode: 'unknown' })

  await n.peer.call('session.watch', { session_id: 'session-p@child-1', since_seq: 2 })
  emitOn(ctl, ev, 'child-1', [text(2, 'assistant', 'found it')], { parentSession: 'session-p', origin: 'subagent' })
  await until('the subagent\'s live batch', () => n.all('session.items').some((b) => b.session_id === 'session-p@child-1'))
  const batch = n.all('session.items').find((b) => b.session_id === 'session-p@child-1')
  assert.deepEqual(batch.items.map((i) => i.text), ['found it'])
  assert.ok(!n.all('session.items').some((b) => b.session_id === 'child-1'), 'nothing under the child\'s own id')

  // Its approval goes on the parent's card, saying which subagent asks.
  n.resolve = () => ({ allow: true })
  const agent = { session: { id: 'child-1', header: { parentSession: 'session-p', origin: 'subagent' } } }
  emitOn(ctl, ev, 'child-1', [{ type: 'assistant/message', seq: 3, time: 0, data: { message: { role: 'assistant', content: [{ type: 'tool-call', id: 'call_rm', name: 'bash', arguments: '{"command":"rm -rf out"}' }] } } }],
    { parentSession: 'session-p', origin: 'subagent' })
  assert.equal(await ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'call_rm', reason: 'deletes files' }, () => 'rejected'), 'allowed-once')
  const card = n.asked.at(-1)
  assert.equal(card.session_id, 'session-p')
  assert.equal(card.summary, 'rm -rf out')
  assert.match(card.reason, /^子 Agent「.+」：deletes files$/)

  // Its question too.
  n.resolve = () => ({ answers: { 'Which one?': 'A' } })
  const res = await ev.waterfall('user-questions/request', { agent, questions: [{ id: 'q', question: 'Which one?', options: [{ label: 'A' }, { label: 'B' }] }] }, () => { throw new Error('no answerer') })
  assert.deepEqual(res, { answers: [{ id: 'q', selected: ['A'] }] })
  assert.equal(n.asked.at(-1).session_id, 'session-p')
  assert.match(n.asked.at(-1).questions[0].header, /^子 Agent「/)

  // Its parent manages it: no sending to or renaming a subagent from the phone.
  await assert.rejects(n.peer.call('session.send', { session_id: 'session-p@child-1', text: 'hi' }), /unsupported/)
  await assert.rejects(n.peer.call('session.rename', { session_id: 'session-p@child-1', title: 'x' }), /unsupported/)
})

test('subagents started before the plugin are named from the parent\'s catalog; older jobs come from the registry', async (t) => {
  const jobs = { list: (caller) => caller === 'session-r' ? [
    { id: 'workflow-old', owner: 'session-r', kind: 'workflow', label: 'nightly review', status: 'running', startedAt: 1_700_000_000_000 },
    { id: 'bash-old', owner: 'session-r', kind: 'bash', label: 'sleep 600', status: 'running', startedAt: 1_700_000_000_001 },
    { id: 'bash-done', owner: 'session-r', kind: 'bash', label: 'ls', status: 'completed', startedAt: 1_700_000_000_002 },
  ] : [] }
  const { n, ctl, rt } = await setup(t, { jobs })
  ctl.add('session-r', { cwd: '/w/app' }, [toolResult(0, 'call_old', 'started subagent child-9')],
    { subagentCatalog: [{ childId: 'child-9', childCreatedAt: 1_700_000_000_000, mode: 'continuable', label: 'Old one' }] })
  ctl.add('child-9', { cwd: '/w/app', origin: 'subagent', parentSessionId: 'session-r', running: true })
  rt.start()
  await n.ready
  await until('named from the catalog', () => rowOf(n, 'session-r')?.subagents?.[0]?.description === 'Old one')
  const row = rowOf(n, 'session-r')
  assert.equal(row.subagents[0].tool_use_id, 'call_old')
  assert.deepEqual(row.background.map((b) => [b.id, b.kind, b.command]), [['workflow-old', 'agent', undefined], ['bash-old', 'shell', 'sleep 600']])
})

test('an approval card offers 本会话总是同意; always still answers DSH with allowed-once, a refusal still rejects (#3974)', async (t) => {
  const { n, ctl, ev, rt } = await setup(t, { liveClient: false })
  ctl.add('session-al', {})
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  const agent = { session: { id: 'session-al' } }
  const browser = () => { throw new Error('the browser was asked') }

  n.resolve = () => ({ allow: true, always: true })
  assert.equal(await ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'call_a' }, browser), 'allowed-once')
  const card = n.asked.at(-1)
  assert.equal(card.always_label, '本会话（主 Agent 和子 Agent）不再问 Bash')
  assert.equal(n.hello.caps.keep_always, true, 'DSH asks the hub to keep it')
  assert.equal(card.always_keys, undefined, 'the node names the grant, not the runtime')

  n.resolve = () => ({ allow: false, always: true })
  assert.equal(await ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'call_b' }, browser), 'rejected')
})

test('a session with no open turn and running background commands reports background and done status (#5981)', async (t) => {
  const { n, ctl, rt } = await setup(t)
  ctl.add('session-bg', {
    cwd: '/w/app',
    running: true, // DSH controller reports running: true because of the background task
    projections: {
      kind: 'cached',
      asOfSeq: 10,
      values: {
        title: 'Background job session',
        turnBoundary: { openTurnStartSeq: null, lastStepBoundary: { kind: 'end' }, lastTurn: 2 },
        approvalState: { turnOpen: false },
        contextTimeline: {
          commands: [
            { id: 'cmd-bg-1', background: true, status: 'running', command: 'sleep 300', startedAt: 1_700_000_000_000 },
          ],
        },
      },
    },
  })
  rt.start()
  await n.ready
  await until('session-bg is listed', () => rowOf(n, 'session-bg'))
  const row = rowOf(n, 'session-bg')
  assert.equal(row.status, 'done', 'foreground status should be done when no turn is open')
  assert.deepEqual(row.background, [{
    id: 'cmd-bg-1',
    kind: 'shell',
    description: 'sleep 300',
    command: 'sleep 300',
    started_at: new Date(1_700_000_000_000).toISOString(),
  }])
})

test('a session waiting on a DSH Web dialog reports blocked with attention reason (#5980)', async (t) => {
  const { n, ctl, ev, rt } = await setup(t) // default: liveClient = true, interactions = auto
  ctl.add('session-dialog', { cwd: '/w/app', running: true })
  rt.start()
  await n.ready
  await until('session-dialog is listed', () => rowOf(n, 'session-dialog'))
  assert.equal(rowOf(n, 'session-dialog')?.status, 'working')

  // When an approval is handed to Web GUI and still pending
  let resolveBrowser
  const browserPromise = new Promise((resolve) => { resolveBrowser = resolve })
  const agent = { session: { id: 'session-dialog' } }

  const waterfallPromise = ev.waterfall('approval/request', { agent, toolName: 'bash', callId: 'call_dialog' }, () => browserPromise)

  await until('session-dialog is marked blocked', () => rowOf(n, 'session-dialog')?.status === 'blocked')
  const blockedRow = rowOf(n, 'session-dialog')
  assert.equal(blockedRow.status, 'blocked')
  assert.equal(blockedRow.attention_reason, 'waiting for an answer in DSH Web')

  // When Web GUI settles the request
  resolveBrowser('allowed-once')
  assert.equal(await waterfallPromise, 'allowed-once')

  await until('session-dialog clears blocked state', () => rowOf(n, 'session-dialog')?.status !== 'blocked')
  const settledRow = rowOf(n, 'session-dialog')
  assert.equal(settledRow.attention_reason, undefined)
})

test('updated_at advances with session events and timeContext even when controller.list keeps lastPromptAt (#11893)', async (t) => {
  const { n, ctl, ev, rt } = await setup(t)
  const now = Date.now()
  const promptAt = now - 3 * 3_600_000
  const otherAt = now - 600_000
  const finishAt = now - 5_000
  const projAt = now - 30_000

  ctl.add('session-long', { cwd: '/w/app', updatedAt: promptAt, running: true })
  ctl.add('session-other', { cwd: '/w/app', updatedAt: otherAt, running: false })
  ctl.add('session-proj', {
    cwd: '/w/app',
    updatedAt: now - 10 * 86_400_000,
    running: false,
    projections: { kind: 'cached', asOfSeq: 5, values: { timeContext: { lastMessageTime: projAt } } },
  })
  rt.start()
  await n.ready
  await until('initial list', () => rowOf(n, 'session-long') && rowOf(n, 'session-proj'))
  assert.equal(rowOf(n, 'session-proj').updated_at, new Date(projAt).toISOString())

  // DSH keeps summary.updatedAt at lastPromptAt while the turn finishes hours later.
  ctl.sessions.get('session-long').summary.running = false
  emitOn(ctl, ev, 'session-long', [
    { type: 'assistant/message', seq: 10, time: finishAt - 1000, data: { message: { role: 'assistant', content: [{ type: 'text', text: 'PR opened' }] } } },
    { type: 'turn/end', seq: 11, time: finishAt, data: { reason: { kind: 'completed' } } },
  ])
  await until('updated_at reflects turn/end', () => rowOf(n, 'session-long')?.updated_at === new Date(finishAt).toISOString())
  const list = n.last('runtime.sessions').sessions
  assert.equal(list[0].session_id, 'session-long', 'newly finished long turn ranks first')
  assert.equal(list[0].status, 'done')
})

test('answerOf preserves multiSelect options containing commas and keeps mixed custom text intact', () => {
  const questions = [
    {
      id: 'q-perms',
      question: 'Which permissions?',
      multiSelect: true,
      options: [{ label: 'Read, write' }, { label: 'Delete' }, { label: 'Admin, billing' }],
    },
    {
      id: 'q-db',
      question: 'Which databases?',
      multiSelect: true,
      options: [{ label: 'pg' }, { label: 'sqlite' }],
    },
  ]

  assert.deepEqual(
    answerOf(questions, undefined, {
      answers: {
        'Which permissions?': 'Read, write',
        'Which databases?': 'pg, sqlite',
      },
    }),
    [
      { id: 'q-perms', selected: ['Read, write'] },
      { id: 'q-db', selected: ['pg', 'sqlite'] },
    ],
  )

  assert.deepEqual(
    answerOf(questions, undefined, {
      answers: {
        'Which permissions?': 'Read, write, Admin, billing',
        'Which databases?': 'pg, cockroach',
      },
    }),
    [
      { id: 'q-perms', selected: ['Read, write', 'Admin, billing'] },
      { id: 'q-db', selected: [], custom: 'pg, cockroach' },
    ],
  )
})
