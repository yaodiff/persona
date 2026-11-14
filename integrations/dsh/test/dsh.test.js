import { mcpToolNames } from '../../runtime-sdk/lib/compat.js'
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createDshRuntime } from '../lib/dsh.js'

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
  const n = await startFakeNode(t, { tools: opts.tools })
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
  ] } } })
  assert.equal(await ask('session-asst', 'w1'), 'allowed-once')
  assert.equal(n.asked.length, 1, 'a memory write is not asked')
  assert.equal(await ask('session-asst', 'w2'), 'allowed-once')
  assert.equal(n.asked.length, 2, 'a write elsewhere is')
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
  const plainTools = n0.tools
  const withAssistant = [...plainTools, { name: 'ask_user', description: 'Ask', input_schema: { type: 'object' } }]
  rt.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  // The fake node lists the same tools whatever is asked: make it answer as a hub does.
  n.peer.onRequest = ((orig) => async (m, p) => (m === 'mcp.tools' ? { tools: p?.assistant ? withAssistant : plainTools } : orig(m, p)))(n.peer.onRequest)
  await rt.refreshTools()
  await until('registered', () => registered.size === 2 * mcpToolNames('example').length)
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


