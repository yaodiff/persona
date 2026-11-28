import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { syncBuiltinESMExports } from 'node:module'
import os from 'node:os'
import { dirname, join, sep } from 'node:path'
import { RpcError } from '../../runtime-sdk/index.js'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createPaseoBridge, TIMELINES_MAX, uuidOf } from '../lib/bridge.js'
import { PaseoClient } from '../lib/paseo.js'
import { agentSnapshot, autoAcceptFeature, entry, mcpServersError, startFakePaseo, workspaceDescriptor } from './fake-paseo.js'

const SID = 'aaaaaaaa-0000-4000-8000-000000000001'
const SID2 = 'aaaaaaaa-0000-4000-8000-000000000002'
const FEATURES = ['prefer_running', 'resume_argv', 'handover', 'transcript', 'launchers', 'start_params', 'modes', 'resume']

const PROVIDERS = { claude: { enabled: false }, 'my-claude': { extends: 'claude', label: 'My Claude' }, 'my-codex': { extends: 'codex' }, grok: { extends: 'acp', label: 'Grok' } }
const ENTRIES = [
  { provider: 'claude', enabled: false, status: 'unavailable', models: [] },
  { provider: 'my-claude', enabled: true, status: 'ready', label: 'My Claude', models: [
    { id: 'fable[1m]', label: 'Fable', isDefault: true, thinkingOptions: [{ id: 'low' }, { id: 'high' }, { id: 'max' }], defaultThinkingOptionId: 'max' },
  ] },
  { provider: 'my-codex', enabled: true, status: 'ready', label: 'My Codex', models: [{ id: 'gpt', label: 'GPT', isDefault: true }] },
  { provider: 'grok', enabled: true, status: 'ready', label: 'Grok', models: [] },
]
// macOS keeps /tmp behind a link to /private/tmp; the bridge creates an Agent in the resolved folder.
const TMP = realpathSync('/tmp')
const PROJECT = { projectKey: 'prj_1', projectName: 'proj', workspaceName: '主线', checkout: { cwd: '/tmp', isGit: false } }

function agents() {
  const claude = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'A', status: 'idle', lastUserMessageAt: '2026-11-04T00:00:00Z',
    persistence: { provider: 'my-claude', sessionId: SID, metadata: { provider: 'claude' } }, runtimeInfo: { provider: 'claude', sessionId: SID },
    availableModes: [{ id: 'default' }, { id: 'acceptEdits' }, { id: 'plan' }, { id: 'bypassPermissions' }], model: 'fable[1m]' })
  const codex = agentSnapshot({ provider: 'my-codex', cwd: '/tmp', title: 'B', status: 'running', persistence: { provider: 'my-codex', sessionId: 'th-1', metadata: { provider: 'codex' } } })
  const grok = agentSnapshot({ provider: 'grok', cwd: '/tmp', title: 'C', status: 'closed' })
  const fresh = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: null })
  const labeled = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'E', labels: { 'persona.session': 'paseo-e1' },
    persistence: { provider: 'my-claude', sessionId: SID2, metadata: { provider: 'claude' } } })
  return { claude, codex, grok, fresh, labeled }
}

async function setup(t, o = {}) {
  const a = agents()
  const fake = await startFakePaseo(t, { providers: PROVIDERS, entries: ENTRIES, agents: Object.values(a).map((agent) => ({ agent, project: PROJECT })), ...o })
  const node = await startFakeNode(t, { features: o.nodeFeatures ?? FEATURES, strict: o.strict })
  const home = mkdtempSync('/tmp/ppaseo-')
  t.after(() => rmSync(home, { recursive: true, force: true }))
  // Where Paseo's own CLI finds its daemon: the address in the home's paseo.pid.
  writeFileSync(join(home, 'paseo.pid'), JSON.stringify({ pid: process.pid, listen: `127.0.0.1:${fake.port}` }))
  const logs = []
  const paseo = new PaseoClient({ home, retryMin: 30, retryMax: 100, pingEvery: o.pingEvery, silentFor: o.silentFor, log: (m) => logs.push(m) })
  // The fake node answers a card at once, as no person can: such a dismissal is taken for a person's unless a test
  // says how soon the hub's own answer comes (atOnceMs).
  const bridge = createPaseoBridge({ paseo, log: (m) => logs.push(m) },
    { socketPath: node.path, mcpConfig: o.mcpConfig, allowTools: o.allowTools, retryMs: 30, pushDelayMs: 5, refreshMs: 0, awayGrace: o.awayGrace ?? 400, callWait: o.callWait ?? 1500,
      startGiveUp: o.startGiveUp, listMax: o.listMax, pageSize: o.pageSize, closedChecks: o.closedChecks, sweepMs: o.sweepMs ?? 0, atOnceMs: o.atOnceMs ?? 0,
      resumeWaitMs: o.resumeWaitMs })
  bridge.start()
  t.after(() => bridge.stop())
  await node.ready
  await until('the first list', () => node.last('runtime.sessions')?.sessions?.length >= 4)
  return { fake, node, bridge, paseo, logs, a, rows: () => node.last('runtime.sessions').sessions }
}

const call = (node, method, params) => node.peer.call(method, params)

test("Paseo's agents are the engine's sessions: Claude Code's by its session and transcript, the others by agent", async (t) => {
  const { node, a, rows } = await setup(t)
  assert.equal(node.hello.runtime, 'paseo')
  assert.equal(node.hello.name, 'Paseo')
  assert.equal(node.hello.version, '0.14.0', "Paseo's version")
  assert.deepEqual(node.hello.caps, { start: true, send: true, interrupt: true, queue_edit: false, queue_send_now: false, rename: true, close: true, assistant: false, modes: true, model: true,
    models: true, check: true, rewind: true, seen: true, workspaces: true, resume: true, readonly: true, same_kind: true })
  assert.deepEqual(node.hello.launchers.map((l) => [l.name, l.agent, l.prompt_optional]),
    [['my-claude', 'claude', undefined], ['my-codex', 'codex', true], ['grok', 'paseo', undefined]])
  assert.deepEqual(node.hello.launchers.map((l) => l.auto_accept), [undefined, undefined, true], "Grok's approval is Paseo's Auto Accept (#8141)")
  // Named as in Paseo; the providers that are not Claude Code's say what a start takes (#8261).
  assert.deepEqual(node.hello.launchers.map((l) => [l.display_name, l.permission_modes]),
    [['My Claude', undefined], ['My Codex', ['default', 'bypassPermissions']], ['Grok', ['default', 'bypassPermissions']]])
  assert.deepEqual(node.hello.launchers[0].models, [{ id: 'fable[1m]', name: 'Fable', default: true, efforts: ['low', 'high', 'max'], default_effort: 'max' }])
  const byId = Object.fromEntries(rows().map((r) => [r.session_id, r]))
  assert.deepEqual(Object.keys(byId).sort(), [SID, 'paseo-e1', `paseo-${a.codex.id}`, `paseo-${a.grok.id}`].sort(), 'a Claude agent without a session yet is not listed')
  assert.deepEqual([byId[SID].agent, byId[SID].transcript, byId[SID].status, byId[SID].launcher], ['claude', SID, 'done', 'my-claude'])
  assert.deepEqual([byId[SID].space_id, byId[SID].space_name, byId[SID].slot_id, byId[SID].slot_name], ['prj_1', 'proj', undefined, '主线'],
    "the project is the space; Paseo's workspace names where in it, and is no slot")
  assert.deepEqual([byId['paseo-e1'].transcript, byId['paseo-e1'].title], [SID2, 'E'])
  assert.deepEqual([byId[`paseo-${a.codex.id}`].agent, byId[`paseo-${a.codex.id}`].status, byId[`paseo-${a.codex.id}`].transcript], ['codex', 'working', undefined])
  assert.deepEqual([byId[`paseo-${a.grok.id}`].agent_label, byId[`paseo-${a.grok.id}`].status], ['Grok', 'offline'])
})

test('changes in Paseo move the list: a new agent, a status, one archived', async (t) => {
  const { fake, node, a, rows } = await setup(t)
  const n = agentSnapshot({ provider: 'grok', cwd: '/tmp', title: 'new', status: 'running' })
  fake.upsert(n, PROJECT)
  await until('the new agent listed', () => rows().some((r) => r.session_id === `paseo-${n.id}` && r.status === 'working'))
  fake.update(a.codex.id, { status: 'idle', lastUserMessageAt: '2026-11-04T01:00:00Z' })
  await until('the codex agent done', () => rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.status === 'done')
  fake.update(a.fresh.id, { runtimeInfo: { provider: 'claude', sessionId: 'bbbbbbbb-0000-4000-8000-000000000003' } })
  await until('the Claude agent listed once it has a session', () => rows().some((r) => r.session_id === 'bbbbbbbb-0000-4000-8000-000000000003' && !r.transcript))
  await call(node, 'session.close', { session_id: `paseo-${n.id}` })
  assert.equal(fake.requestsOf('archive_agent_request').at(-1).agentId, n.id)
  await until('the archived agent gone', () => !rows().some((r) => r.session_id === `paseo-${n.id}`))
})

test("an agent's timeline as items, its live text, and what it writes next while watched", async (t) => {
  const { fake, node, a } = await setup(t)
  const id = `paseo-${a.codex.id}`
  fake.setTimeline(a.codex.id, [
    entry(1, { type: 'user_message', text: '跑测试', messageId: 'u1' }),
    entry(2, { type: 'tool_call', callId: 'c1', name: 'shell', status: 'running', error: null, detail: { type: 'shell', command: 'go test ./...' } }),
  ])
  const p = await call(node, 'session.items', { session_id: id, turns: 3 })
  assert.deepEqual(p.items.map((i) => [i.id, i.kind]), [['1#0', 'text'], ['2#0', 'tool_use']])
  assert.equal(p.items[1].tool_name, 'Bash')
  assert.equal(p.history, `paseo:${a.codex.id}:ep1`)

  await call(node, 'session.watch', { session_id: id, since_seq: 0 })
  await until('the timeline followed', () => fake.requestsOf('agent.timeline.set_subscription.request').some((r) => r.agentIds[0] === a.codex.id))
  await until('the subscription made', () => [...fake.subs.values()].some((s) => s.kind === 'timeline'))
  fake.stream(a.codex.id, { type: 'timeline', provider: 'codex', turnId: 't1', item: { type: 'assistant_message', text: '测试', messageId: 'm1' } }, 5)
  fake.stream(a.codex.id, { type: 'timeline', provider: 'codex', turnId: 't1', item: { type: 'assistant_message', text: '都过了', messageId: 'm1' } }, 6)
  await until('the live text', () => node.all('session.delta').some((d) => d.session_id === id && d.text === '测试都过了'))

  // The tool finished and the text was written: the next read pushes them, from where the watch began.
  fake.setTimeline(a.codex.id, [
    entry(1, { type: 'user_message', text: '跑测试', messageId: 'u1' }),
    entry(2, { type: 'tool_call', callId: 'c1', name: 'shell', status: 'completed', error: null, detail: { type: 'shell', command: 'go test ./...', output: 'ok', exitCode: 0 } }, 4),
    entry(5, { type: 'assistant_message', text: '测试都过了' }, 6),
  ])
  fake.update(a.codex.id, { status: 'idle', lastUserMessageAt: '2026-11-04T01:00:00Z' })
  fake.stream(a.codex.id, { type: 'turn_completed', provider: 'codex', turnId: 't1' })
  const pushed = await until('the new items', () => node.all('session.items').filter((x) => x.session_id === id).flatMap((x) => x.items).length >= 2 &&
    node.all('session.items').filter((x) => x.session_id === id))
  const all = pushed.flatMap((x) => x.items)
  assert.deepEqual(all.map((i) => [i.id, i.kind]), [['4#0', 'tool_result'], ['5#0', 'text']])
  assert.equal(pushed[0].from_seq, 2, 'continuous with where the watch began')
  assert.equal(pushed.at(-1).to_seq, 6)
  assert.ok(node.all('session.delta').some((d) => d.session_id === id && d.text === ''), 'the live text ends')
  await call(node, 'session.unwatch', { session_id: id })
  await until('the subscription released', () => fake.requestsOf('subscription.release.request').length > 0)
})

test('whole timelines are kept for the watched agents and the most recently read ones only', async (t) => {
  const many = Array.from({ length: 40 }, (_, i) => agentSnapshot({ provider: 'my-codex', cwd: '/tmp', title: `T${i}`, status: 'idle' }))
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  const { fake, node, bridge } = await setup(t, { agents: [...base, ...many.map((agent) => ({ agent, project: PROJECT }))] })
  for (const [i, x] of many.entries()) {
    fake.setTimeline(x.id, [entry(1, { type: 'user_message', text: `question ${i}`, messageId: `u${i}` }), entry(2, { type: 'assistant_message', text: `answer ${i}` })])
  }
  const sid = (x) => `paseo-${x.id}`
  const reads = (x) => fake.requestsOf('fetch_agent_timeline_request').filter((r) => r.agentId === x.id && r.limit === 0).length
  const kept = bridge.state.timelines
  const [watched, ...others] = many
  const first = await call(node, 'session.items', { session_id: sid(watched), turns: 3 })
  await call(node, 'session.watch', { session_id: sid(watched), since_seq: 0 })
  const pages = []
  for (const x of others) pages.push(await call(node, 'session.items', { session_id: sid(x), turns: 3 }))
  assert.ok(kept.size <= TIMELINES_MAX, `${kept.size} whole timelines kept after reading ${many.length} agents`)
  assert.ok(kept.has(watched.id), "the watched agent's is kept, though read first")
  assert.ok(!kept.has(others[0].id) && kept.has(others.at(-1).id), 'the least recently read go first')

  // A read from the cache counts as recent: the next one to go is the one after it.
  const oldest = others.find((x) => kept.has(x.id))
  const next = others[others.indexOf(oldest) + 1]
  const before = reads(oldest)
  await call(node, 'session.items', { session_id: sid(oldest), turns: 3 })
  assert.equal(reads(oldest), before, 'read from the cache')
  const again = await call(node, 'session.items', { session_id: sid(others[0]), turns: 3 })
  assert.deepEqual(again, pages[0], 'one that went is read from Paseo again, the same')
  assert.ok(kept.has(oldest.id) && !kept.has(next.id), 'the one read from the cache stays; the next least recently read went')
  assert.ok(kept.size <= TIMELINES_MAX)

  // The watched session pages from its kept timeline, as before.
  const watchedReads = reads(watched)
  assert.deepEqual(await call(node, 'session.items', { session_id: sid(watched), turns: 3 }), first)
  assert.equal(reads(watched), watchedReads, 'not read from Paseo again')
  assert.deepEqual(first.items.map((i) => i.text), ['question 0', 'answer 0'])
})

test('a rewind in Paseo goes to the phone as another history, even when the timeline got shorter', async (t) => {
  const { fake, node, a } = await setup(t)
  const id = `paseo-${a.codex.id}`
  fake.setTimeline(a.codex.id, [
    entry(1, { type: 'user_message', text: '一', messageId: 'u1' }),
    entry(2, { type: 'assistant_message', text: '答一' }),
    entry(3, { type: 'user_message', text: '二', messageId: 'u2' }),
    entry(4, { type: 'assistant_message', text: '答二' }),
  ])
  await call(node, 'session.watch', { session_id: id, since_seq: 0 })
  await until('the subscription made', () => [...fake.subs.values()].some((s) => s.kind === 'timeline'))
  // Rewound to before the second entry: a new epoch, two entries.
  fake.setTimeline(a.codex.id, [
    entry(1, { type: 'user_message', text: '一', messageId: 'u1' }),
    entry(2, { type: 'assistant_message', text: '答一' }),
  ], 'ep2')
  fake.emit('timeline', { type: 'agent.timeline.replacement', payload: { agentId: a.codex.id, epoch: 'ep2' } }, (s) => s.agentIds.includes(a.codex.id))
  const b = await until('another history', () => node.all('session.items').find((x) => x.session_id === id && x.history === `paseo:${a.codex.id}:ep2`))
  assert.ok(b.to_seq <= 2, 'the shorter timeline, not the old end')
  // What it writes next goes on from there.
  fake.setTimeline(a.codex.id, [
    entry(1, { type: 'user_message', text: '一', messageId: 'u1' }),
    entry(2, { type: 'assistant_message', text: '答一' }),
    entry(3, { type: 'user_message', text: '二，重来', messageId: 'u3' }),
  ], 'ep2')
  fake.stream(a.codex.id, { type: 'timeline', provider: 'codex', turnId: 't2', item: { type: 'user_message', text: '二，重来', messageId: 'u3' } }, 3)
  const next = await until('the next entry', () => node.all('session.items').find((x) => x.session_id === id && x.items.some((i) => i.text === '二，重来')))
  assert.equal(next.history, `paseo:${a.codex.id}:ep2`)
  assert.equal(next.from_seq, b.to_seq)
})

test("a Claude Code session's watch is its live text only: the node reads the transcript", async (t) => {
  const { fake, node, a } = await setup(t)
  await call(node, 'session.watch', { session_id: SID, since_seq: 0, transcript: true })
  await until('the subscription made', () => [...fake.subs.values()].some((s) => s.kind === 'timeline' && s.agentIds[0] === a.claude.id))
  fake.stream(a.claude.id, { type: 'timeline', provider: 'claude', turnId: 't', item: { type: 'assistant_message', text: '好的', messageId: 'msg_1' } }, 10)
  await until('the live text', () => node.all('session.delta').some((d) => d.session_id === SID && d.text === '好的'))
  fake.stream(a.claude.id, { type: 'timeline', provider: 'claude', turnId: 't', item: { type: 'tool_call', callId: 'toolu_1', name: 'Bash', status: 'running', error: null, detail: { type: 'shell', command: 'ls' } } }, 11)
  await until('it ends at the tool call', () => node.all('session.delta').some((d) => d.session_id === SID && d.text === ''))
  await new Promise((r) => setTimeout(r, 300))
  assert.equal(node.all('session.items').filter((x) => x.session_id === SID).length, 0, 'no items: the transcript has them')
  assert.equal(fake.requestsOf('fetch_agent_timeline_request').filter((r) => r.agentId === a.claude.id).length, 0, 'its timeline is not read')
})

test('an agent Paseo let go is loaded only by opening its chat: a background read asks Paseo nothing (#8132)', async (t) => {
  const { fake, node, bridge, a } = await setup(t)
  const id = `paseo-${a.grok.id}` // closed: Paseo collected its idle runtime
  fake.setTimeline(a.grok.id, [entry(1, { type: 'user_message', text: '查日志', messageId: 'u1' }), entry(2, { type: 'assistant_message', text: '查完了' })])
  const reads = (agentId) => fake.requestsOf('fetch_agent_timeline_request').filter((r) => r.agentId === agentId).length
  // The manager's read after a turn, an app's background sync of its recent chats: refused, nothing asked of Paseo.
  for (const params of [{ turns: 1 }, { after_seq: 0 }, { limit: 1 }]) {
    await assert.rejects(call(node, 'session.items', { session_id: id, ...params }), (e) => e.code === 'not_loaded', JSON.stringify(params))
  }
  await assert.rejects(call(node, 'session.item', { session_id: id, item_id: '2#0' }), (e) => e.code === 'not_loaded')
  assert.equal(reads(a.grok.id), 0, 'no timeline request: Paseo would load the agent to answer it')
  assert.deepEqual(fake.loads, [])

  // A person opens its chat: the watch loads it, once, and the chat shows its history.
  await call(node, 'session.watch', { session_id: id, since_seq: 0 })
  assert.deepEqual(fake.loads, [a.grok.id], 'loaded by the watch')
  const page = await call(node, 'session.items', { session_id: id, turns: 3 })
  assert.deepEqual(page.items.map((i) => i.text), ['查日志', '查完了'])
  assert.equal(page.history, `paseo:${a.grok.id}:ep1`)
  await call(node, 'session.unwatch', { session_id: id })

  // An hour later Paseo lets it go again, its updatedAt as it was: a background read is answered with the last read.
  await until('loaded', () => bridge.state.agents.get(a.grok.id)?.agent.status === 'idle')
  fake.upsert({ ...fake.agents.get(a.grok.id).agent, status: 'closed' })
  await until('let go again', () => bridge.state.agents.get(a.grok.id)?.agent.status === 'closed')
  const before = reads(a.grok.id)
  assert.deepEqual((await call(node, 'session.items', { session_id: id, turns: 3 })).items, page.items, 'all of it: a stored conversation does not change')
  assert.equal(reads(a.grok.id), before)
  // Once it moved while stored (renamed in Paseo), the last read may not be all of it: refused again.
  fake.update(a.grok.id, { title: 'C2' })
  await until('moved', () => bridge.state.agents.get(a.grok.id)?.agent.title === 'C2')
  await assert.rejects(call(node, 'session.items', { session_id: id, turns: 1 }), (e) => e.code === 'not_loaded')
  assert.equal(reads(a.grok.id), before)
  assert.deepEqual(fake.loads, [a.grok.id])

  // A Claude agent whose transcript Claude Code's cleanup removed: the node, finding no file, asks the bridge. Its
  // timeline is read the same way, only while its chat is open.
  fake.upsert({ ...fake.agents.get(a.claude.id).agent, status: 'closed' })
  await until('the Claude agent let go', () => bridge.state.agents.get(a.claude.id)?.agent.status === 'closed')
  fake.setTimeline(a.claude.id, [entry(1, { type: 'user_message', text: '一个月前的问题', messageId: 'u1' })])
  await assert.rejects(call(node, 'session.items', { session_id: SID, turns: 1 }), (e) => e.code === 'not_loaded')
  assert.equal(reads(a.claude.id), 0)
  await call(node, 'session.watch', { session_id: SID, since_seq: 0 })
  assert.deepEqual((await call(node, 'session.items', { session_id: SID, turns: 3 })).items.map((i) => i.text), ['一个月前的问题'])
  assert.deepEqual(fake.loads, [a.grok.id, a.claude.id])
})

test('a watched chat whose agent Paseo lets go reads nothing more until the agent runs again (#8132)', async (t) => {
  const { fake, node, bridge, a } = await setup(t)
  const id = `paseo-${a.codex.id}` // running
  const reads = () => fake.requestsOf('fetch_agent_timeline_request').filter((r) => r.agentId === a.codex.id).length
  fake.setTimeline(a.codex.id, [entry(1, { type: 'user_message', text: '跑测试', messageId: 'u1' })])
  await call(node, 'session.watch', { session_id: id, since_seq: 0 })
  await until('the subscription made', () => [...fake.subs.values()].some((s) => s.kind === 'timeline'))
  fake.setTimeline(a.codex.id, [entry(1, { type: 'user_message', text: '跑测试', messageId: 'u1' }), entry(2, { type: 'assistant_message', text: '都过了' })])
  fake.update(a.codex.id, { status: 'idle', lastUserMessageAt: '2026-11-04T01:00:00Z' })
  await until('the end of the turn pushed', () => node.all('session.items').some((x) => x.session_id === id && x.items.some((i) => i.text === '都过了')))
  const before = reads()
  // The chat stays open; an hour idle, Paseo collects the agent's runtime. Reading it now would load it again.
  fake.upsert({ ...fake.agents.get(a.codex.id).agent, status: 'closed' })
  await until('let go', () => bridge.state.agents.get(a.codex.id)?.agent.status === 'closed')
  await new Promise((r) => setTimeout(r, 400)) // past the watch's refresh delay
  assert.equal(reads(), before, 'nothing read once Paseo let it go')
  assert.deepEqual(fake.loads, [])
  // A message has Paseo load and run it: its next turn is read as it runs.
  fake.setTimeline(a.codex.id, [entry(1, { type: 'user_message', text: '跑测试', messageId: 'u1' }), entry(2, { type: 'assistant_message', text: '都过了' }),
    entry(3, { type: 'user_message', text: '再跑一次', messageId: 'u2' })])
  fake.update(a.codex.id, { status: 'running' })
  await until('the next turn pushed', () => node.all('session.items').some((x) => x.session_id === id && x.items.some((i) => i.text === '再跑一次')))
  assert.deepEqual(fake.loads, [], 'read while Paseo had it loaded')
})

test('an archived agent kept as a row is loaded only by opening its chat (#8132)', async (t) => {
  const thread = '0199aaaa-bbbb-7ccc-8ddd-eeeeffff0009'
  const kept = agentSnapshot({ id: 'kept-codex', provider: 'my-codex', status: 'closed', labels: { 'persona.session': 'paseo-kept-codex' },
    archivedAt: new Date().toISOString(), persistence: { provider: 'my-codex', sessionId: thread, metadata: { provider: 'codex' } } })
  const { fake, node, rows } = await setup(t, { agents: [...Object.values(agents()).map((agent) => ({ agent, project: PROJECT })), { agent: kept, project: PROJECT }] })
  await until('the retained Codex row', () => rows().some((r) => r.session_id === 'paseo-kept-codex'))
  fake.setTimeline(kept.id, [entry(1, { type: 'user_message', text: '归档前', messageId: 'u1' }), entry(2, { type: 'assistant_message', text: '做完了' })])
  await assert.rejects(call(node, 'session.items', { session_id: 'paseo-kept-codex', turns: 1 }), (e) => e.code === 'not_loaded')
  assert.deepEqual(fake.loads, [], 'Paseo would load an archived agent to read it, as history')
  await call(node, 'session.watch', { session_id: 'paseo-kept-codex', since_seq: 0 })
  assert.deepEqual((await call(node, 'session.items', { session_id: 'paseo-kept-codex', turns: 3 })).items.map((i) => i.text), ['归档前', '做完了'])
  assert.deepEqual(fake.loads, [kept.id])
  assert.ok(fake.agents.get(kept.id).agent.archivedAt, 'still archived: reading it restores nothing')
})

test('a watched session another agent takes on: the watch follows that agent, and its content is another history', async (t) => {
  const { fake, node, bridge, a } = await setup(t)
  // A Claude session (its items are the transcript's): the live text of the agent that has it now.
  fake.update(a.claude.id, { status: 'closed' })
  await until('closed', () => bridge.state.agents.get(a.claude.id)?.agent.status === 'closed')
  await call(node, 'session.watch', { session_id: SID, since_seq: 0, transcript: true })
  await until('followed', () => [...fake.subs.values()].some((s) => s.kind === 'timeline' && s.agentIds.includes(a.claude.id)))
  const b = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'A again', status: 'running',
    persistence: { provider: 'my-claude', sessionId: SID, metadata: { provider: 'claude' } }, runtimeInfo: { provider: 'claude', sessionId: SID } })
  fake.upsert(b, PROJECT)
  await until('the new agent followed', () => [...fake.subs.values()].some((s) => s.kind === 'timeline' && s.agentIds.includes(b.id)))
  assert.equal(bridge.state.watches.get(SID).agentId, b.id)
  assert.ok(fake.requestsOf('subscription.release.request').length > 0, 'the old agent is let go')
  fake.stream(b.id, { type: 'timeline', provider: 'claude', turnId: 't1', item: { type: 'assistant_message', text: '接着说', messageId: 'm1' } }, 5)
  await until('its live text', () => node.all('session.delta').some((d) => d.session_id === SID && d.text === '接着说'))

  // An agent whose items are its timeline: the new one's goes out as another history.
  const id = 'paseo-e1' // the labeled agent's session
  fake.setTimeline(a.labeled.id, [entry(1, { type: 'user_message', text: '一', messageId: 'u1' }), entry(2, { type: 'assistant_message', text: '答一' }),
    entry(3, { type: 'user_message', text: '二', messageId: 'u2' })])
  await call(node, 'session.watch', { session_id: id, since_seq: 0 })
  await until('followed', () => [...fake.subs.values()].some((s) => s.kind === 'timeline' && s.agentIds.includes(a.labeled.id)))
  fake.update(a.labeled.id, { status: 'closed' })
  const c = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'E again', status: 'running', labels: { 'persona.session': id },
    persistence: { provider: 'my-claude', sessionId: SID2, metadata: { provider: 'claude' } } })
  fake.setTimeline(c.id, [entry(1, { type: 'user_message', text: '一', messageId: 'u1' })])
  fake.upsert(c, PROJECT)
  const pushed = await until('another history', () => node.all('session.items').find((x) => x.session_id === id && x.history?.startsWith(`paseo:${c.id}:`)))
  assert.equal(pushed.to_seq, 1)
  assert.equal(pushed.from_seq, undefined, 'not a continuation of the old agent')
  // The phone reads the new agent's page, then it writes on: pushed from its own seq, not the old agent's.
  await call(node, 'session.items', { session_id: id, turns: 3 })
  fake.setTimeline(c.id, [entry(1, { type: 'user_message', text: '一', messageId: 'u1' }), entry(2, { type: 'assistant_message', text: '新答' })])
  fake.update(c.id, { status: 'idle' })
  fake.stream(c.id, { type: 'turn_completed', provider: 'claude', turnId: 't2' })
  const next = await until('the new agent writes on', () => node.all('session.items').find((x) => x.session_id === id && x.items.some((i) => i.text === '新答')))
  assert.equal(next.from_seq, 1)
  assert.equal(next.history, pushed.history)
})

test('sending: into the running turn, once per message, images as Paseo takes them', async (t) => {
  const { fake, node, a } = await setup(t)
  await call(node, 'session.send', { session_id: SID, text: '继续', client_msg_id: 'cm-1', images: [{ mime: 'image/png', data: 'iVBO' }] })
  await call(node, 'session.send', { session_id: SID, text: '继续', client_msg_id: 'cm-1' })
  const sent = fake.requestsOf('send_agent_message_request')
  assert.equal(sent.length, 1, 'a retry of the same message is not sent again')
  assert.deepEqual({ ...sent[0], requestId: undefined }, { type: 'send_agent_message_request', requestId: undefined, agentId: a.claude.id, text: '继续',
    messageId: uuidOf(`persona:${a.claude.id}:cm-1`), activeTurnBehavior: 'steer', images: [{ data: 'iVBO', mimeType: 'image/png' }] })
  fake.handlers.send_agent_message_request = () => ({ payload: { agentId: a.claude.id, accepted: false, error: 'Agent is archived' } })
  await assert.rejects(call(node, 'session.send', { session_id: SID, text: 'x', client_msg_id: 'cm-2' }), /Paseo 没有收下这条消息：Agent is archived/)
  await assert.rejects(call(node, 'session.send', { session_id: 'paseo-nope', text: 'x' }), /没有这个会话/)
  // Paseo slow to start the run (a closed agent loading): the send is left with it, not failed.
  fake.handlers.send_agent_message_request = () => null
  const t0 = Date.now()
  await call(node, 'session.send', { session_id: SID, text: '慢', client_msg_id: 'cm-3' })
  assert.ok(Date.now() - t0 >= 1400, 'waited for Paseo first')
})

test('interrupt, rename, permission mode and effort go to the agent', async (t) => {
  const { fake, node, a } = await setup(t)
  await call(node, 'session.interrupt', { session_id: SID })
  assert.equal(fake.requestsOf('cancel_agent_request')[0].agentId, a.claude.id)
  await call(node, 'session.rename', { session_id: SID, title: '新名字' })
  assert.equal(fake.requestsOf('update_agent_request')[0].name, '新名字')
  await call(node, 'session.modes', { session_id: SID, permission_mode: 'plan', effort: 'high' })
  assert.deepEqual([fake.requestsOf('set_agent_mode_request')[0].modeId, fake.requestsOf('set_agent_thinking_request')[0].thinkingOptionId], ['plan', 'high'])
  await assert.rejects(call(node, 'session.modes', { session_id: SID, effort: 'xhigh' }), /没有「xhigh」/)
  await assert.rejects(call(node, 'session.modes', { session_id: SID, permission_mode: 'auto' }), /没有「auto」模式/)
  await assert.rejects(call(node, 'session.modes', { session_id: `paseo-${a.codex.id}`, permission_mode: 'plan' }), /没有这个权限模式/)
  // Codex: Persona's bypassPermissions is Paseo's full-access; the other modes are Paseo's own (left alone).
  await call(node, 'session.modes', { session_id: `paseo-${a.codex.id}`, permission_mode: 'bypassPermissions' })
  assert.equal(fake.requestsOf('set_agent_mode_request').at(-1).modeId, 'full-access')
})

test("a Codex agent leaves Allow all for its provider's own mode: the default Paseo names, else its first short of full access (#8261)", async (t) => {
  const own = await setup(t, { entries: ENTRIES.map((e) => e.provider === 'my-codex' ? { ...e, defaultModeId: 'auto-review' } : e) })
  const modeOf = (fake) => fake.requestsOf('set_agent_mode_request').at(-1)?.modeId
  await call(own.node, 'session.modes', { session_id: `paseo-${own.a.codex.id}`, permission_mode: 'default' })
  assert.equal(modeOf(own.fake), 'auto-review', "Paseo's default for that provider (its reviewer on)")

  const { fake, node, a, rows } = await setup(t)
  const sid = `paseo-${a.codex.id}`
  fake.update(a.codex.id, { currentModeId: 'full-access', availableModes: [{ id: 'auto' }, { id: 'full-access' }] })
  await until('Allow all', () => rows().find((r) => r.session_id === sid)?.permission_mode === 'bypassPermissions')
  await call(node, 'session.modes', { session_id: sid, permission_mode: 'default' })
  assert.equal(modeOf(fake), 'auto', 'no default named: the first of its modes short of full access')
  assert.deepEqual(rows().find((r) => r.session_id === sid).capabilities.permission_modes, ['default', 'bypassPermissions'])
})

test("the model goes to the agent first, an effort is the new model's; an unknown model, or an archived agent, changes nothing", async (t) => {
  const { fake, node, a, bridge } = await setup(t)
  fake.entries = ENTRIES.map((e) => e.provider !== 'my-claude' ? e : { ...e, models: [...e.models,
    { id: 'haiku', label: 'Haiku', aliases: ['haiku-latest'], thinkingOptions: [{ id: 'low' }] }] })
  fake.emit('events', { type: 'providers_snapshot_update', payload: { entries: fake.entries } }, (s) => s.events.includes('providers_snapshot_update'))
  await until('the new model listed', () => node.last('runtime.launchers')?.launchers?.[0]?.models?.length === 2)
  await call(node, 'session.modes', { session_id: SID, model: 'haiku', effort: 'low' })
  assert.deepEqual(fake.requestsOf('set_agent_model_request').map((r) => [r.agentId, r.modelId]), [[a.claude.id, 'haiku']])
  assert.equal(fake.requestsOf('set_agent_thinking_request')[0].thinkingOptionId, 'low')
  const order = fake.requests.filter((r) => r.type === 'set_agent_model_request' || r.type === 'set_agent_thinking_request').map((r) => r.type)
  assert.deepEqual(order, ['set_agent_model_request', 'set_agent_thinking_request'], 'the model before its effort')
  await call(node, 'session.modes', { session_id: SID, model: 'haiku-latest' })
  assert.equal(fake.requestsOf('set_agent_model_request').at(-1).modelId, 'haiku-latest', 'an alias Paseo lists')
  await assert.rejects(call(node, 'session.modes', { session_id: SID, model: 'haiku', effort: 'max' }), /没有「max」/, "an effort the new model lacks")
  await assert.rejects(call(node, 'session.modes', { session_id: SID, model: 'nope' }), /has no model nope/)
  fake.handlers.set_agent_model_request = () => ({ payload: { agentId: a.claude.id, accepted: false, error: 'busy' } })
  await assert.rejects(call(node, 'session.modes', { session_id: SID, model: 'fable[1m]' }), /did not switch the model: busy/)
  delete fake.handlers.set_agent_model_request
  const sent = fake.requestsOf('set_agent_model_request').length
  fake.archive(a.labeled.id) // archived, it keeps its id (Persona's label): still a session, whose model Paseo cannot change
  await until('the archive noted', () => bridge.state.archived.has(a.labeled.id))
  await assert.rejects(call(node, 'session.modes', { session_id: 'paseo-e1', model: 'haiku' }), /archived in Paseo/)
  assert.equal(fake.requestsOf('set_agent_model_request').length, sent, 'nothing sent for an archived agent')
})

test("rows say how far a rewind goes: Paseo's provider decides, conversation and files apart", async (t) => {
  const { fake, a, rows } = await setup(t)
  fake.update(a.claude.id, { capabilities: { supportsRewindConversation: true, supportsRewindFiles: true, supportsRewindBoth: true } })
  fake.update(a.codex.id, { capabilities: { supportsRewindConversation: true } })
  fake.update(a.grok.id, { capabilities: { supportsRewindBoth: true } })
  await until('the rows', () => rows().find((r) => r.session_id === SID)?.capabilities?.rewind_code === true)
  const caps = (id) => rows().find((r) => r.session_id === id)?.capabilities
  assert.deepEqual(caps(SID), { rewind: true, rewind_code: true })
  await until('codex', () => caps(`paseo-${a.codex.id}`)?.rewind === true)
  // (Both also say the permission modes they take, #8261.)
  const both = ['default', 'bypassPermissions']
  assert.deepEqual(caps(`paseo-${a.codex.id}`), { rewind: true, rewind_code: false, permission_modes: both }, 'Codex: the conversation only')
  assert.deepEqual(caps(`paseo-${a.grok.id}`), { rewind: false, rewind_code: false, permission_modes: both, a2a_wake_on_send: true },
    'files and conversation only at once is no rewind of Persona\'s')
  assert.deepEqual(caps('paseo-e1'), { rewind: false, rewind_code: false }, 'a Paseo that does not say')
})

test("an archived agent's row offers nothing Paseo refuses for it: no rewind, mode, effort or model (#8133)", async (t) => {
  const { fake, node, a, bridge, rows } = await setup(t)
  const row = () => rows().find((r) => r.session_id === 'paseo-e1')
  fake.update(a.labeled.id, { capabilities: { supportsRewindConversation: true, supportsRewindBoth: true } })
  await until('rewind offered', () => row()?.capabilities?.rewind_code === true)
  assert.deepEqual(row().capabilities, { rewind: true, rewind_code: true }, 'listed: modes and model are the node\'s to give')
  await call(node, 'session.close', { session_id: 'paseo-e1' })
  await until('archived, still its row', () => bridge.state.archived.has(a.labeled.id) && row()?.status === 'offline')
  assert.deepEqual(row().capabilities, { rewind: false, rewind_code: false, modes: false, model: false },
    'the capabilities it was archived with are not what Paseo does with an archived agent')
  assert.equal(row().efforts, undefined)
  for (const params of [{ permission_mode: 'plan' }, { effort: 'high' }, { model: 'fable[1m]' }]) {
    await assert.rejects(call(node, 'session.modes', { session_id: 'paseo-e1', ...params }), /archived in Paseo/, JSON.stringify(params))
  }
  await assert.rejects(call(node, 'session.rewind', { session_id: 'paseo-e1', prompt: 'x', occurrence: 1, restore: 'conversation' }), /archived in Paseo/)
})

test("a row that is not Claude Code's lists the efforts its model takes; one Paseo has no thinking options for lists none (#8133)", async (t) => {
  const thinking = [{ id: 'low' }, { id: 'medium' }, { id: 'high' }, { id: 'ultra' }]
  const entries = ENTRIES.map((e) => e.provider === 'grok' ? { ...e, models: [{ id: 'grok-4', label: 'Grok 4', isDefault: true, thinkingOptions: thinking }] }
    : e.provider === 'my-codex' ? { ...e, models: [{ id: 'gpt', label: 'GPT', isDefault: true, thinkingOptions: [{ id: 'xhigh' }, { id: 'low' }] }] } : e)
  const { a, rows } = await setup(t, { entries })
  const row = (id) => rows().find((r) => r.session_id === id)
  assert.deepEqual(row(`paseo-${a.grok.id}`).efforts, ['low', 'medium', 'high'], "its model's levels that are Persona's")
  assert.deepEqual(row(`paseo-${a.codex.id}`).efforts, ['low', 'xhigh'], 'weakest first')
  assert.equal(row(SID).efforts, undefined, "Claude Code's levels are the apps' own")
})

test('a rewind goes to the message the app saw, keeps the session id through Claude Code\'s fork, and refuses a conversation that moved', async (t) => {
  const { fake, node, a, rows } = await setup(t)
  fake.update(a.claude.id, { capabilities: { supportsRewindConversation: true, supportsRewindBoth: true } })
  fake.setTimeline(a.claude.id, [
    entry(1, { type: 'user_message', text: 'again', messageId: 'u1' }), entry(2, { type: 'assistant_message', text: 'one' }),
    entry(3, { type: 'user_message', text: '<persona-context kind="selection">x</persona-context>\nagain', messageId: 'u2' }),
    entry(4, { type: 'assistant_message', text: 'two' }),
    entry(5, { type: 'user_message', text: '[Request interrupted by user]', messageId: 'u3' }),
    entry(6, { type: 'user_message', text: 'last  one', messageId: 'u4' }), entry(7, { type: 'assistant_message', text: 'three' }),
  ])
  fake.handlers['agent.rewind.request'] = (msg) => {
    // Claude Code's rewind forks its session: the agent moves to the fork's id.
    fake.update(msg.agentId, { persistence: { provider: 'my-claude', sessionId: SID2, metadata: { provider: 'claude' } } })
    return { payload: { agentId: msg.agentId, ok: true, error: null } }
  }
  await until('rewind offered', () => rows().find((r) => r.session_id === SID)?.capabilities?.rewind === true)
  const res = await call(node, 'session.rewind', { session_id: SID, prompt: 'again', occurrence: 1, prompts_after: 1, restore: 'code_and_conversation' })
  assert.deepEqual(res, { prompt: 'again', restore: 'code_and_conversation', code_restored: true, input_cleared: true })
  assert.deepEqual(fake.requestsOf('agent.rewind.request').map((r) => [r.agentId, r.messageId, r.mode]), [[a.claude.id, 'u2', 'both']],
    "the last 'again', its context block aside; an interruption is no message of the user's")
  assert.deepEqual(fake.requestsOf('update_agent_request').map((r) => r.labels), [{ 'persona.session': SID, 'persona.kept': '1' }],
    'its id kept on the label first, marked as kept by the bridge (not an Agent Persona started)')
  await until('the fork listed', () => rows().find((r) => r.session_id === SID)?.transcript === SID2)
  assert.equal(rows().filter((r) => r.session_id === SID).length, 1, 'the same session, now in the fork')
  // The first 'again' (two after it), restoring only the conversation.
  await call(node, 'session.rewind', { session_id: SID, prompt: 'again', occurrence: 2, prompts_after: 2, restore: 'conversation' })
  assert.deepEqual(fake.requestsOf('agent.rewind.request').at(-1), { ...fake.requestsOf('agent.rewind.request').at(-1), messageId: 'u1', mode: 'conversation' })
  assert.equal(fake.requestsOf('update_agent_request').length, 1, 'a label once')
  const sent = fake.requestsOf('agent.rewind.request').length
  await assert.rejects(call(node, 'session.rewind', { session_id: SID, prompt: 'again', occurrence: 1, prompts_after: 0, restore: 'conversation' }), /moved/)
  await assert.rejects(call(node, 'session.rewind', { session_id: SID, prompt: 'never said', occurrence: 1, restore: 'conversation' }), /moved/)
  // Codex: the conversation only; a provider that says nothing: none.
  fake.update(a.codex.id, { capabilities: { supportsRewindConversation: true }, status: 'idle' })
  await until('codex idle', () => rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.status !== 'working')
  await assert.rejects(call(node, 'session.rewind', { session_id: `paseo-${a.codex.id}`, prompt: 'x', occurrence: 1, restore: 'code_and_conversation' }), /cannot rewind/)
  await assert.rejects(call(node, 'session.rewind', { session_id: 'paseo-e1', prompt: 'x', occurrence: 1, restore: 'conversation' }), /cannot rewind/)
  fake.update(a.claude.id, { status: 'running' })
  await until('running', () => rows().find((r) => r.session_id === SID)?.status === 'working')
  await assert.rejects(call(node, 'session.rewind', { session_id: SID, prompt: 'again', occurrence: 1, restore: 'conversation' }), /current turn/)
  fake.update(a.claude.id, { status: 'idle' })
  fake.handlers['agent.rewind.request'] = (msg) => ({ payload: { agentId: msg.agentId, ok: false, error: 'Cannot rewind before the provider acknowledges the submitted prompt' } })
  await until('idle', () => rows().find((r) => r.session_id === SID)?.status !== 'working')
  await assert.rejects(call(node, 'session.rewind', { session_id: SID, prompt: 'last one', occurrence: 1, prompts_after: 0, restore: 'conversation' }), /did not rewind: Cannot rewind/)
  assert.equal(fake.requestsOf('agent.rewind.request').length, sent + 1, 'only the last one reached Paseo')
})

test("a launcher's models come from Paseo when asked, refreshed first when the picker says so", async (t) => {
  const { fake, node } = await setup(t)
  fake.handlers.refresh_providers_snapshot_request = () => ({ payload: { acknowledged: true } })
  fake.handlers.list_provider_models_request = (msg) => ({ payload: { provider: msg.provider, fetchedAt: new Date().toISOString(), error: null,
    models: [{ id: 'opus', label: 'Opus', isDefault: true, thinkingOptions: [{ id: 'high' }, { id: 'turbo' }] }, { id: 'old', label: 'Old', isSelectable: false }] } })
  const got = await call(node, 'launchers.models', { launcher: 'my-claude' })
  assert.deepEqual(got, { agent: 'claude', models: [{ id: 'opus', name: 'Opus', default: true, efforts: ['high'] }] })
  assert.equal(fake.requestsOf('refresh_providers_snapshot_request').length, 0)
  await call(node, 'launchers.models', { launcher: 'my-codex', refresh: true })
  assert.deepEqual(fake.requests.filter((r) => /refresh_providers|list_provider_models/.test(r.type)).map((r) => [r.type, r.providers ?? r.provider]),
    [['list_provider_models_request', 'my-claude'], ['refresh_providers_snapshot_request', ['my-codex']], ['list_provider_models_request', 'my-codex']])
  fake.handlers.list_provider_models_request = (msg) => ({ payload: { provider: msg.provider, fetchedAt: new Date().toISOString(), error: 'Provider is not available' } })
  await assert.rejects(call(node, 'launchers.models', { launcher: 'grok' }), /not available/)
  await assert.rejects(call(node, 'launchers.models', { launcher: 'claude' }), /no provider claude/, 'a provider that cannot start agents')
})

test("Settings › Models' test: one real prompt through Paseo's provider, its probe never a session, everything it made cleaned up", async (t) => {
  const { fake, node, rows } = await setup(t)
  const dir = mkdtempSync('/tmp/ppaseo-check-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const projects = [{ projectId: 'prj_1' }]
  fake.handlers['project.list.request'] = () => ({ payload: { projects } })
  let made = 'prj_check'
  fake.handlers['workspace.create.request'] = () => ({ payload: { workspace: { id: 'wks_check', projectId: made }, setupTerminalId: null, error: null } })
  let finish = { status: 'idle', final: { model: 'fable[1m]' }, error: null, lastMessage: ' OK ' }
  fake.handlers.wait_for_finish_request = () => ({ payload: finish })
  fake.handlers.delete_agent_request = (msg) => ({ type: 'agent_deleted', payload: { agentId: msg.agentId } })
  fake.handlers.archive_workspace_request = (msg) => ({ payload: { workspaceId: msg.workspaceId, archivedAt: new Date().toISOString(), error: null } })
  fake.handlers['project.remove.request'] = (msg) => ({ payload: { projectId: msg.projectId, accepted: true, removedWorkspaceIds: [], error: null } })
  const res = await call(node, 'launchers.check', { launcher: 'my-claude', cwd: dir, prompt: 'Say OK' })
  assert.equal(res.ok, true)
  assert.deepEqual([res.agent, res.model, res.answer], ['claude', 'fable[1m]', 'OK'])
  const ws = fake.requestsOf('workspace.create.request')[0]
  assert.deepEqual([ws.source, ws.background], [{ kind: 'directory', path: dir }, true], "a background workspace: Paseo's app does not list it")
  const probe = fake.requestsOf('agent.create.request')[0]
  assert.deepEqual([probe.workspaceId, probe.config.provider, probe.config.modeId, probe.initialPrompt, probe.labels], ['wks_check', 'my-claude', 'plan', 'Say OK', { 'persona.check': '1' }])
  assert.equal(probe.config.mcpServers, undefined, 'no Persona tools')
  assert.ok(!rows().some((r) => r.cwd === dir), 'the probe is no session')
  assert.equal(fake.requestsOf('delete_agent_request')[0].agentId, probe.agentId)
  assert.equal(fake.requestsOf('archive_workspace_request')[0].workspaceId, 'wks_check')
  assert.equal(fake.requestsOf('project.remove.request')[0].projectId, 'prj_check', 'the project the check made')
  // A directory Paseo files under a project it had: that project stays. A probe that asks instead of answering fails.
  made = 'prj_1'
  finish = { status: 'permission', final: null, error: null, lastMessage: null }
  const again = await call(node, 'launchers.check', { launcher: 'my-codex', cwd: dir, prompt: 'Say OK' })
  assert.deepEqual([again.ok, again.agent], [false, 'codex'])
  assert.match(again.error, /asked for a permission/)
  assert.equal(fake.requestsOf('agent.create.request')[1].config.modeId, undefined, 'plan mode only where the provider has it')
  assert.equal(fake.requestsOf('delete_agent_request').length, 2)
  assert.equal(fake.requestsOf('project.remove.request').length, 1, "a project Paseo had is not removed")
  await assert.rejects(call(node, 'launchers.check', { launcher: 'nope', cwd: dir, prompt: 'Say OK' }), /no provider nope/)
})

test("seen in Persona, no longer unread in Paseo: a loaded agent's mark cleared, a stored one's through its workspace, never one that asks", async (t) => {
  const { fake, node, a, rows } = await setup(t)
  fake.handlers.clear_agent_attention = (msg) => ({ payload: { agentId: msg.agentId, agents: [] } })
  fake.handlers['workspace.clear_attention.request'] = (msg) => ({ payload: { workspaceId: msg.workspaceId, clearedAgentIds: [a.grok.id], results: [], success: true, error: null } })
  // A legacy row with a past turn already says done before the bridge receives the attention flag.
  fake.update(a.claude.id, { requiresAttention: false })
  await until('seen in Paseo', () => rows().find((r) => r.session_id === SID)?.status === 'idle')
  fake.update(a.claude.id, { requiresAttention: true })
  await until('unread', () => rows().find((r) => r.session_id === SID)?.status === 'done')
  await call(node, 'session.seen', { session_id: SID })
  assert.deepEqual(fake.requestsOf('clear_agent_attention').map((r) => r.agentId), [a.claude.id])
  // Stored (closed): through its workspace, while it is the only one flagged there; not the agent's own (it would start it).
  fake.update(a.claude.id, { requiresAttention: false })
  fake.update(a.grok.id, { requiresAttention: true, status: 'closed' })
  await until('the grok flagged', () => rows().find((r) => r.session_id === `paseo-${a.grok.id}`)?.status === 'done')
  await until('known flagged', () => rows().find((r) => r.session_id === SID)?.status === 'idle')
  await call(node, 'session.seen', { session_id: `paseo-${a.grok.id}` })
  assert.deepEqual(fake.requestsOf('workspace.clear_attention.request').map((r) => r.workspaceId), ['wks_1'])
  assert.equal(fake.requestsOf('clear_agent_attention').length, 1, 'never clear_agent_attention for a stored agent')
  fake.update(a.labeled.id, { requiresAttention: true })
  await until('another flagged', () => rows().find((r) => r.session_id === 'paseo-e1')?.status === 'done')
  await call(node, 'session.seen', { session_id: `paseo-${a.grok.id}` })
  assert.equal(fake.requestsOf('workspace.clear_attention.request').length, 1, 'not while another agent of the workspace is flagged')
  // Waiting for an answer, or not flagged: nothing.
  fake.permission(a.labeled.id, { id: 'p-1', provider: 'claude', name: 'Bash', kind: 'tool', input: { command: 'ls' } })
  await until('blocked', () => rows().find((r) => r.session_id === 'paseo-e1')?.status === 'blocked')
  await call(node, 'session.seen', { session_id: 'paseo-e1' })
  await call(node, 'session.seen', { session_id: SID })
  assert.equal(fake.requestsOf('clear_agent_attention').length, 1)
})

const MCP_JSON = {
  mcpServers: {
    persona: { command: '/usr/bin/personad', args: ['mcp'], env: { PERSONA_HOME: '/home/u/.persona' } },
    legacy: { command: '/usr/bin/personad', args: ['mcp'] },
  },
}

test("a new session gets Persona's agent tools, each server naming the session the agent is kept under", async (t) => {
  const dir = mkdtempSync('/tmp/ppaseo-mcp-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const mcpConfig = join(dir, 'mcp.json')
  writeFileSync(mcpConfig, JSON.stringify(MCP_JSON))
  const { fake, node } = await setup(t, { mcpConfig })
  for (const launcher of ['my-claude', 'my-codex']) {
    await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher })
    const req = fake.requestsOf('agent.create.request').at(-1)
    const sessionId = req.labels['persona.session']
    assert.equal(sessionId, `paseo-${req.agentId}`)
    assert.deepEqual(req.config.mcpServers, {
      persona: { type: 'stdio', command: '/usr/bin/personad', args: ['mcp'], env: { PERSONA_HOME: '/home/u/.persona', PERSONA_SESSION_ID: sessionId } },
      legacy: { type: 'stdio', command: '/usr/bin/personad', args: ['mcp'], env: { PERSONA_SESSION_ID: sessionId } },
    })
  }
  const [first, second] = fake.requestsOf('agent.create.request').map((r) => r.config.mcpServers.persona.env.PERSONA_SESSION_ID)
  assert.notEqual(first, second, 'each agent has its own session id')
  assert.equal(JSON.parse(readFileSync(mcpConfig, 'utf8')).mcpServers.persona.env.PERSONA_SESSION_ID, undefined, 'the shared config is not changed')
})

test('a Persona-started Paseo agent receives its own A2A MCP ticket', async (t) => {
  const dir = mkdtempSync('/tmp/ppaseo-mcp-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const mcpConfig = join(dir, 'mcp.json')
  writeFileSync(mcpConfig, JSON.stringify(MCP_JSON))
  const { fake, node, logs } = await setup(t, { mcpConfig, nodeFeatures: [...FEATURES, 'a2a_mcp'] })
  const ticketRequests = []
  node.a2aMcpToken = (id) => { ticketRequests.push(id); return 'test-a2a-ticket' }
  const sessionId = 'paseo-a2a-test'
  await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-codex', session_id: sessionId })
  const { mcpServers } = fake.requestsOf('agent.create.request').at(-1).config
  assert.deepEqual(ticketRequests, [sessionId])
  assert.equal(mcpServers.persona.env.PERSONA_A2A_TOKEN, undefined)
  assert.equal(mcpServers.persona_a2a.env.PERSONA_SESSION_ID, sessionId)
  assert.equal(mcpServers.persona_a2a.env.PERSONA_A2A_TOKEN, 'test-a2a-ticket')
  assert.ok(logs.every((line) => !line.includes('test-a2a-ticket')))
})

test("a new Claude Code session gets the Agent's own Persona tools preapproved; a provider that cannot is created again without that", async (t) => {
  const dir = mkdtempSync('/tmp/ppaseo-mcp-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const mcpConfig = join(dir, 'mcp.json')
  writeFileSync(mcpConfig, JSON.stringify(MCP_JSON))
  const own = ['mcp__persona__report_to_supervisor', 'mcp__persona__todo_decision_add', 'mcp__jarvis__report_to_supervisor']
  const { fake, node, logs } = await setup(t, { mcpConfig, allowTools: own })
  await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-claude' })
  assert.deepEqual(fake.requestsOf('agent.create.request').at(-1).config.toolPolicy, { preapproved: [
    { kind: 'mcp', server: 'persona', tool: 'report_to_supervisor' }, { kind: 'mcp', server: 'persona', tool: 'todo_decision_add' },
  ] }, "only the servers it is given (none under the former name here)")
  await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-codex' })
  assert.equal(fake.requestsOf('agent.create.request').at(-1).config.toolPolicy, undefined, 'Codex: Paseo would limit the server to those tools')
  fake.handlers['agent.create.request'] = (msg) => msg.config.toolPolicy
    ? { payload: { agent: null, error: `Provider '${msg.config.provider}' cannot preapprove exact MCP tools for unattended execution` } }
    : undefined
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-claude' })
  const [first, second] = fake.requestsOf('agent.create.request').slice(-2)
  assert.ok(first.config.toolPolicy && !second.config.toolPolicy && second.config.mcpServers, 'again with the tools, without the preapproval')
  assert.equal(second.labels['persona.session'], first.labels['persona.session'], 'the same Persona session')
  assert.equal(res.session.session_id, first.labels['persona.session'])
  assert.ok(logs.some((l) => /cannot preapprove/.test(l)))
})

test("the fake daemon takes config.mcpServers only in Paseo's stdio shape", () => {
  const ok = { type: 'stdio', command: 'personad', args: ['mcp'], env: { A: 'b' } }
  assert.equal(mcpServersError({ persona: ok }), null)
  assert.equal(mcpServersError({ persona: { command: 'personad' } }), 'mcpServers.persona.type: expected stdio, http or sse')
  assert.match(mcpServersError({ persona: { ...ok, command: 1 } }), /command/)
  assert.match(mcpServersError({ persona: { ...ok, args: ['a', 2] } }), /args/)
  assert.match(mcpServersError({ persona: { ...ok, env: { A: 1 } } }), /env/)
})

test("an Agent for a provider that takes no MCP servers is created again without them, once, and only for that error", async (t) => {
  const dir = mkdtempSync('/tmp/ppaseo-mcp-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const mcpConfig = join(dir, 'mcp.json')
  writeFileSync(mcpConfig, JSON.stringify(MCP_JSON))
  const { fake, node, logs, rows } = await setup(t, { mcpConfig, noMcpProviders: ['grok'] })
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'grok' })
  const [first, second, ...more] = fake.requestsOf('agent.create.request')
  assert.equal(more.length, 0, 'one more request, not a loop')
  assert.ok(first.config.mcpServers.persona.env.PERSONA_SESSION_ID)
  assert.deepEqual(second.config, { provider: 'grok', cwd: TMP }, 'the old configuration, no mcpServers')
  assert.notEqual(second.agentId, first.agentId, 'a new agent id: the failed one has a record in Paseo')
  assert.equal(first.labels['persona.session'], `paseo-${first.agentId}`)
  assert.equal(second.labels['persona.session'], first.labels['persona.session'], 'the Persona session stays; only the Paseo agent is new')
  assert.equal(res.session.session_id, first.labels['persona.session'])
  assert.ok(logs.some((l) => /provider grok takes no MCP servers; creating the agent again without the Persona tools/.test(l)), logs.join('\n'))
  await until('listed', () => rows().some((r) => r.session_id === res.session.session_id))
  assert.equal(fake.agents.has(first.agentId), false, 'the failed attempt left no agent')
  assert.equal(fake.agents.has(second.agentId), true)
  assert.deepEqual([...fake.agents.values()].filter((e) => e.agent.provider === 'grok' && e.agent.labels['persona.session']).map((e) => e.agent.id), [second.agentId], 'one new agent, not two')

  // Any other failure is the answer, not a reason to try again.
  const before = fake.requestsOf('agent.create.request').length
  fake.handlers['agent.create.request'] = () => ({ payload: { agent: null, error: 'Provider unavailable' } })
  await assert.rejects(call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-claude' }), /Provider unavailable/)
  assert.equal(fake.requestsOf('agent.create.request').length, before + 1)

  // Without servers to give there is nothing to take away: the failure is final.
  const none = await setup(t, { noMcpProviders: ['grok'], mcpConfig: join(dir, 'absent.json') })
  none.fake.handlers['agent.create.request'] = () => ({ payload: { agent: null, error: "Provider 'grok' does not support MCP servers" } })
  await assert.rejects(call(none.node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'grok' }), /does not support MCP servers/)
  assert.equal(none.fake.requestsOf('agent.create.request').length, 1)
})

test("a refusal that comes after the start was answered keeps the session id the row, the label and the tools named", async (t) => {
  const dir = mkdtempSync('/tmp/ppaseo-mcp-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const mcpConfig = join(dir, 'mcp.json')
  writeFileSync(mcpConfig, JSON.stringify(MCP_JSON))
  const { fake, node, rows } = await setup(t, { mcpConfig, noMcpProviders: ['grok'] })
  const known = new Set(rows().map((r) => r.session_id))
  fake.refuseDelay = 2000 // past the bridge's wait for the answer (callWait 1500 in setup)
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'grok' })
  const placeholder = res.session.session_id
  const [first] = fake.requestsOf('agent.create.request')
  assert.equal(fake.requestsOf('agent.create.request').length, 1, 'the row came while the first request was unanswered')
  assert.equal(placeholder, `paseo-${first.agentId}`)
  assert.equal(first.config.mcpServers.persona.env.PERSONA_SESSION_ID, placeholder)
  await until('the second request', () => fake.requestsOf('agent.create.request').length === 2)
  const second = fake.requestsOf('agent.create.request')[1]
  assert.notEqual(second.agentId, first.agentId, 'a new Paseo agent')
  assert.equal(second.config.mcpServers, undefined)
  assert.equal(second.labels['persona.session'], placeholder, 'the same Persona session')
  await until('listed under the row\'s id', () => rows().some((r) => r.session_id === placeholder))
  assert.deepEqual([...fake.agents.values()].filter((e) => e.agent.provider === 'grok' && e.agent.labels['persona.session']).map((e) => [e.agent.id, e.agent.labels['persona.session']]),
    [[second.agentId, placeholder]])
  assert.equal(rows().filter((r) => !known.has(r.session_id) && r.session_id !== placeholder).length, 0, 'no second session id')
})

test("the row of an agent Paseo is still creating names its provider as its listed row will (#8142)", async (t) => {
  const { fake, node } = await setup(t)
  fake.handlers['agent.create.request'] = () => null // Paseo still starting the provider: past the bridge's wait (callWait)
  const grok = (await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'grok' })).session
  assert.deepEqual([grok.agent, grok.agent_label, grok.launcher], ['paseo', 'Grok', 'grok'], "a provider the apps don't know: its label, not the engine's name")
  const claude = (await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-claude' })).session
  assert.deepEqual([claude.agent, claude.agent_label, claude.launcher], ['claude', undefined, 'my-claude'], 'Claude Code: the apps name it themselves')
})

test('a new session without a usable MCP config is created as before, and the log says why', async (t) => {
  const dir = mkdtempSync('/tmp/ppaseo-mcp-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const missing = await setup(t, { mcpConfig: join(dir, 'absent.json') })
  await call(missing.node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-claude' })
  assert.deepEqual(missing.fake.requestsOf('agent.create.request')[0].config, { provider: 'my-claude', cwd: TMP })
  assert.ok(missing.logs.some((l) => /cannot read the MCP config .*absent\.json.*no Persona tools/.test(l)), missing.logs.join('\n'))
  const unset = await setup(t)
  await call(unset.node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-claude' })
  assert.equal(unset.fake.requestsOf('agent.create.request')[0].config.mcpServers, undefined)
  assert.ok(unset.logs.some((l) => /no MCP config given/.test(l)))
})

test('a new session: an agent of the launcher\'s provider, with the form\'s choices, under an id kept on it', async (t) => {
  const { fake, node, rows } = await setup(t)
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: '修一下登录', launcher: 'my-claude', model: 'fable[1m]', permission_mode: 'acceptEdits', effort: 'high', workspace_id: 'wks_1' })
  const req = fake.requestsOf('agent.create.request')[0]
  assert.deepEqual(req.config, { provider: 'my-claude', cwd: '/tmp', model: 'fable[1m]', modeId: 'acceptEdits', thinkingOptionId: 'high' })
  assert.equal(req.initialPrompt, '修一下登录')
  assert.equal(req.workspaceId, 'wks_1')
  assert.equal(res.session.session_id, `paseo-${req.agentId}`)
  assert.equal(req.labels['persona.session'], res.session.session_id)
  assert.equal(res.session.agent, 'claude')
  assert.equal(res.instead, undefined, 'everything as asked: nothing to say')
  await until('listed', () => rows().some((r) => r.session_id === res.session.session_id))
  // A launcher of the login shell's that Paseo does not have: Paseo's provider of the same kind, said on the answer.
  const other = await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'cc_gateway', agent_type: 'claude' })
  assert.equal(fake.requestsOf('agent.create.request')[1].config.provider, 'my-claude')
  assert.deepEqual([other.session.launcher, other.instead], ['my-claude', { launcher: 'cc_gateway' }])
  await assert.rejects(call(node, 'session.start', { cwd: '/nonexistent/dir', prompt: 'x' }), /没有这个目录/)
  fake.handlers['agent.create.request'] = () => ({ payload: { agent: null, error: 'Provider unavailable' } })
  await assert.rejects(call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'grok' }), /Paseo 没能新建：Provider unavailable/)
})

test('home paths are expanded before validating and forwarding working directories', async (t) => {
  const { fake, node, paseo } = await setup(t)
  const userHome = realpathSync(mkdtempSync(join(TMP, 'ppaseo-home-')))
  t.after(() => rmSync(userHome, { recursive: true, force: true }))
  const dir = join(userHome, 'sub', 'dir')
  mkdirSync(dir, { recursive: true })
  const homeStub = t.mock.method(os, 'homedir', () => userHome)
  syncBuiltinESMExports()
  t.after(() => {
    homeStub.mock.restore()
    syncBuiltinESMExports()
  })

  const start = (cwd) => call(node, 'session.start', { cwd, prompt: 'x', launcher: 'my-claude' })
  await t.test('start', async () => {
    for (const [input, expected] of [['~', userHome], ['~/sub/dir', dir], [dir, dir]]) {
      await start(input)
      assert.equal(fake.requestsOf('agent.create.request').at(-1).config.cwd, expected, input)
    }
    if (sep === '\\') {
      await start('~\\sub\\dir')
      assert.equal(fake.requestsOf('agent.create.request').at(-1).config.cwd, dir)
    }
  })

  await t.test('refused paths', async () => {
    const before = fake.requestsOf('agent.create.request').length
    await assert.rejects(start('~/missing'), { code: 'bad_request' })
    await assert.rejects(start('~otheruser'), { code: 'bad_request', message: /~otheruser/ })
    if (sep === '/') await assert.rejects(start('~\\sub\\dir'), { code: 'bad_request' })
    assert.equal(fake.requestsOf('agent.create.request').length, before)
  })

  await t.test('worktree creation', async () => {
    writeFileSync(join(paseo.home, 'config.json'), JSON.stringify({ worktrees: { root: '~/trees' } }))
    const { checkouts } = await call(node, 'workspace.prepare', { cwd: dir })
    assert.equal(dirname(checkouts), join(userHome, 'trees'))
    await call(node, 'workspace.create', { cwd: '~/sub/dir', branch: 'feature' })
    assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.cwd, dir)
  })

  await t.test('takeover', async () => {
    await call(node, 'session.start', { cwd: '~/sub/dir', resume: 'cccccccc-0000-4000-8000-000000000009' })
    assert.equal(fake.requestsOf('import_agent_request').at(-1).cwd, dir)
  })

  await t.test('launcher check', async () => {
    fake.handlers.wait_for_finish_request = () => ({ payload: { status: 'idle', lastMessage: 'OK' } })
    const res = await call(node, 'launchers.check', { cwd: '~/sub/dir', launcher: 'my-claude', prompt: 'Say OK' })
    assert.equal(res.ok, true)
    assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.path, dir)
    assert.equal(fake.requestsOf('agent.create.request').at(-1).config.cwd, dir)
  })
})

test("a new Codex session: bypassPermissions starts it with Paseo's full-access mode; the other modes are left alone", async (t) => {
  const { fake, node } = await setup(t)
  const start = (permission_mode) => call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-codex', permission_mode })
    .then(() => fake.requestsOf('agent.create.request').at(-1).config)
  assert.equal((await start('bypassPermissions')).modeId, 'full-access')
  assert.equal((await start('auto')).modeId, undefined)
  assert.equal((await start('default')).modeId, undefined)
})

test("Grok: Allow all starts it with Paseo's Auto Accept on and switches that, the row follows; no other mode reaches it", async (t) => {
  const { fake, node, a, rows } = await setup(t)
  const start = (launcher, permission_mode) => call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher, permission_mode })
  const created = () => fake.requestsOf('agent.create.request').at(-1).config
  const on = await start('grok', 'bypassPermissions')
  assert.deepEqual(created().featureValues, { auto_accept: true })
  assert.equal(on.session.permission_mode, 'bypassPermissions')
  for (const mode of ['default', 'acceptEdits', undefined]) {
    await start('grok', mode)
    assert.equal(created().featureValues, undefined, `${mode}: Paseo's own default (off)`)
  }
  await start('my-claude', 'bypassPermissions')
  assert.deepEqual([created().modeId, created().featureValues], ['bypassPermissions', undefined], "Claude Code's mode, as before")

  const sid = on.session.session_id
  const mode = () => rows().find((r) => r.session_id === sid)?.permission_mode
  const feature = () => fake.requestsOf('set_agent_feature_request').at(-1)
  await call(node, 'session.modes', { session_id: sid, permission_mode: 'default' })
  assert.deepEqual([feature().featureId, feature().value], ['auto_accept', false])
  await until('Ask: no mode said', () => mode() === undefined)
  await call(node, 'session.modes', { session_id: sid, permission_mode: 'bypassPermissions', effort: 'xhigh' })
  assert.equal(feature().value, true)
  await until('Allow all', () => mode() === 'bypassPermissions')
  await assert.rejects(call(node, 'session.modes', { session_id: sid, permission_mode: 'acceptEdits' }), /没有这个权限模式/)
  assert.equal(fake.requestsOf('set_agent_mode_request').length, 0, 'never a mode Grok does not have')
  // One not running is asked too: Paseo loads it first.
  await call(node, 'session.modes', { session_id: `paseo-${a.grok.id}`, permission_mode: 'bypassPermissions' })
  assert.equal(feature().agentId, a.grok.id)
  fake.handlers.set_agent_feature_request = () => ({ payload: { accepted: false, error: 'busy' } })
  await assert.rejects(call(node, 'session.modes', { session_id: sid, permission_mode: 'default' }), /没有换模式：busy/)
})

test("Allow all stays on a row Paseo unloaded or read again after a restart: the stored record lists no features, the last ones seen stand in (#8133)", async (t) => {
  const { fake, a, bridge, rows } = await setup(t)
  const mode = (id) => rows().find((r) => r.session_id === `paseo-${id}`)?.permission_mode
  const other = agentSnapshot({ provider: 'grok', cwd: '/tmp', title: 'D', status: 'idle', features: [autoAcceptFeature(false)] })
  fake.upsert(other, PROJECT)
  fake.update(a.grok.id, { status: 'idle', features: [autoAcceptFeature(true)] })
  await until('Allow all', () => mode(a.grok.id) === 'bypassPermissions' && rows().some((r) => r.session_id === `paseo-${other.id}`))
  // Paseo unloads them: what it sends of an agent it has not loaded is its stored record, with no features
  // (agent-projections buildStoredAgentPayload).
  const store = (id) => {
    const { features: _, ...stored } = fake.agents.get(id).agent
    fake.agents.set(id, { ...fake.agents.get(id), agent: { ...stored, status: 'closed' } })
    fake.upsert(fake.agents.get(id).agent)
  }
  store(a.grok.id)
  store(other.id)
  await until('unloaded', () => bridge.state.agents.get(a.grok.id)?.agent.status === 'closed' && bridge.state.agents.get(other.id)?.agent.status === 'closed')
  await new Promise((r) => setTimeout(r, 50))
  assert.deepEqual([mode(a.grok.id), mode(other.id)], ['bypassPermissions', undefined], 'Allow all on, Ask off: as Paseo still has them')
  // Paseo restarts: its directory lists the stored records again.
  fake.drop()
  await until('connected again', () => fake.hellos.length === 2)
  await until('subscribed again', () => [...fake.subs.values()].some((s) => s.kind === 'agents'))
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(mode(a.grok.id), 'bypassPermissions')
  // Loaded again with the switch off: off, from then on.
  fake.update(a.grok.id, { status: 'idle', features: [autoAcceptFeature(false)] })
  await until('Ask', () => mode(a.grok.id) === undefined)
  store(a.grok.id)
  await until('unloaded again', () => bridge.state.agents.get(a.grok.id)?.agent.status === 'closed')
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(mode(a.grok.id), undefined)
})

// #6192 had a launcher Paseo lacks start on Paseo's default provider (a Claude one) with that provider's own model and
// effort: a Codex launcher started a Claude agent, a gateway ran on another account. The owner's rule since #8140: one
// of the same kind, chosen by Persona and said, or a refusal.
test('a launcher Paseo lacks starts on its provider of the same kind, said on the answer; none of that kind is refused, never another kind (#8140)', async (t) => {
  const { fake, node } = await setup(t, {
    providers: { ...PROVIDERS, codex: { enabled: false } },
    entries: [...ENTRIES, { provider: 'codex', enabled: false, status: 'unavailable', models: [] }],
  })
  // The manager has expanded a task's or agent_start's tier before the node sends session.start; the node names the
  // kind of agent the launcher starts (agent_type).
  const start = (params) => call(node, 'session.start', { cwd: '/tmp', prompt: 'Check the project', permission_mode: 'acceptEdits', workspace_id: 'wks_1', ...params })
  const created = () => fake.requestsOf('agent.create.request').at(-1)
  // A Codex launcher (a preset's, a managed one, the login shell's): Paseo's Codex provider, never its Claude default.
  // The preset's model is the launcher's own, which my-codex does not offer: it runs its default, and the answer says so.
  let res = await start({ launcher: 'codex', agent_type: 'codex', model: 'preset-only-model', effort: 'high' })
  assert.deepEqual(created().config, { provider: 'my-codex', cwd: '/tmp', thinkingOptionId: 'high' })
  assert.equal(created().initialPrompt, 'Check the project')
  assert.equal(created().workspaceId, 'wks_1')
  assert.deepEqual([res.session.launcher, res.session.agent, res.session.model], ['my-codex', 'codex', undefined])
  assert.deepEqual(res.instead, { launcher: 'codex', model: 'preset-only-model' })
  // Paseo's own provider of that name, switched off: its kind, with no word from the node.
  res = await start({ launcher: 'codex' })
  assert.equal(created().config.provider, 'my-codex')
  assert.deepEqual(res.instead, { launcher: 'codex' })
  // A Claude Code gateway of the login shell's: Paseo's Claude Code provider, keeping the model it offers too.
  res = await start({ launcher: 'cc_gateway', agent_type: 'claude', model: 'fable[1m]', effort: 'high' })
  assert.deepEqual(created().config, { provider: 'my-claude', cwd: '/tmp', model: 'fable[1m]', modeId: 'acceptEdits', thinkingOptionId: 'high' })
  assert.deepEqual(res.instead, { launcher: 'cc_gateway' })
  // With no word from the node (one older than #8140), the name says the kind as the node reads it: another name is
  // Claude Code's.
  res = await start({ launcher: 'cc_other' })
  assert.equal(created().config.provider, 'my-claude')
  assert.deepEqual(res.instead, { launcher: 'cc_other' })
  // A kind none of Paseo's providers runs: refused, nothing created; Paseo's catch-all agent names no kind at all.
  const before = fake.requestsOf('agent.create.request').length
  for (const [launcher, agentType] of [['traex', 'traex'], ['traex', undefined], ['gone-acp', 'paseo']]) {
    const err = await start({ launcher, agent_type: agentType }).catch((e) => e)
    assert.equal(err.code, 'launcher_kind_missing', launcher)
    assert.match(err.message, new RegExp(`no provider ${launcher}.*nothing was started`), launcher)
  }
  assert.equal(fake.requestsOf('agent.create.request').length, before)
})

test('an unchanged or unspecified launcher keeps the requested model and effort', async (t) => {
  const { fake, node } = await setup(t)
  for (const params of [{ launcher: 'my-claude' }, { launcher: 'my-claude', shell_launcher: true }, {}, { launcher: '' }]) {
    await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', model: 'fable[1m]', effort: 'high', ...params })
    assert.deepEqual(fake.requestsOf('agent.create.request').at(-1).config, {
      provider: 'my-claude', cwd: TMP, model: 'fable[1m]', thinkingOptionId: 'high',
    })
  }
})

test('a provider in error is replaced only by one of its own kind; with none, the start is refused rather than run on another kind (#8140)', async (t) => {
  const failing = (entries) => entries.map((e) => e.provider === 'my-claude' ? { ...e, status: 'error' } : e)
  const { fake, node } = await setup(t, { entries: failing(ENTRIES) })
  const params = { cwd: '/tmp', prompt: 'x', launcher: 'my-claude', agent_type: 'claude', model: 'fable[1m]', effort: 'high', permission_mode: 'bypassPermissions' }
  const err = await call(node, 'session.start', params).catch((e) => e)
  assert.equal(err.code, 'launcher_kind_missing')
  assert.match(err.message, /my-claude cannot start agents now \(error\)/)
  assert.equal(fake.requestsOf('agent.create.request').length, 0, 'never my-codex in its place')

  // Another Claude Code provider there: it stands in, said on the answer.
  const backup = { provider: 'backup-claude', enabled: true, status: 'ready', models: [{ id: 'fable[1m]', isDefault: true, thinkingOptions: [{ id: 'high' }] }] }
  const second = await setup(t, { providers: { ...PROVIDERS, 'backup-claude': { extends: 'claude' } }, entries: [...failing(ENTRIES), backup] })
  const res = await call(second.node, 'session.start', params)
  assert.deepEqual(second.fake.requestsOf('agent.create.request').at(-1).config,
    { provider: 'backup-claude', cwd: TMP, model: 'fable[1m]', modeId: 'bypassPermissions', thinkingOptionId: 'high' })
  assert.deepEqual(res.instead, { launcher: 'my-claude' })
})

test("an effort the model does not offer goes down to the nearest lower one it offers; the answer says so and the row runs at it (#8140)", async (t) => {
  const { fake, node, rows } = await setup(t)
  const start = (params) => call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-claude', ...params })
  const created = () => fake.requestsOf('agent.create.request').at(-1).config
  // fable[1m], my-claude's default model, offers low, high and max.
  let res = await start({ effort: 'xhigh' })
  assert.deepEqual([created().thinkingOptionId, res.session.effort, res.instead], ['high', 'high', { effort: 'xhigh' }])
  await until('listed at the effort it runs at', () => rows().find((r) => r.session_id === res.session.session_id)?.effort === 'high')
  res = await start({ effort: 'medium', model: 'fable[1m]' })
  assert.deepEqual([created().thinkingOptionId, res.instead], ['low', { effort: 'medium' }])
  res = await start({ effort: 'max' })
  assert.deepEqual([created().thinkingOptionId, res.instead], ['max', undefined])
  // A model whose efforts Paseo has not listed: as asked.
  res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-codex', effort: 'xhigh' })
  assert.deepEqual([created().thinkingOptionId, res.instead], ['xhigh', undefined])
})

test('a read-only (plan) start: Paseo\'s plan mode on a Claude provider, never a writable fallback (#3238)', async (t) => {
  const { fake, node } = await setup(t)
  const plan = (params) => call(node, 'session.start', { cwd: '/tmp', prompt: 'x', permission_mode: 'plan', ...params })
  await plan({ launcher: 'my-claude' })
  assert.equal(fake.requestsOf('agent.create.request').at(-1).config.modeId, 'plan')
  // A Claude Code launcher Paseo does not have: its Claude provider in plan mode, without the model it does not offer.
  const stood = await plan({ launcher: 'cc_gateway', agent_type: 'claude', model: 'preset-only-model', effort: 'high' })
  assert.deepEqual(fake.requestsOf('agent.create.request').at(-1).config, { provider: 'my-claude', cwd: TMP, modeId: 'plan', thinkingOptionId: 'high' })
  assert.deepEqual(stood.instead, { launcher: 'cc_gateway', model: 'preset-only-model' })
  const before = fake.requestsOf('agent.create.request').length
  // A non-Claude provider, or a Codex launcher Paseo lacks (its Codex provider holds no plan mode): refused, never a
  // writable stand-in.
  for (const params of [{ launcher: 'my-codex' }, { launcher: 'grok' }, { launcher: 'codex', agent_type: 'codex' }]) {
    const err = await plan(params).catch((e) => e)
    assert.equal(err.code, 'readonly_unsupported', params.launcher)
  }
  assert.equal(fake.requestsOf('agent.create.request').length, before, 'refused before anything was created')
  // Paseo starting it in another mode after all: archived, and refused.
  fake.handlers['agent.create.request'] = (msg) => ({ payload: { agent: agentSnapshot({ id: msg.agentId, provider: 'my-claude', cwd: '/tmp', currentModeId: 'bypassPermissions' }) } })
  const err = await plan({ launcher: 'my-claude' }).catch((e) => e)
  assert.equal(err.code, 'readonly_unsupported')
  assert.equal(fake.requestsOf('archive_agent_request').at(-1).agentId, fake.requestsOf('agent.create.request').at(-1).agentId)
  // A takeover cannot set the mode: refused unless Paseo's own agent is in plan already.
  const take = await call(node, 'session.start', { cwd: '/tmp', resume: 'cccccccc-0000-4000-8000-000000000009', permission_mode: 'plan' }).catch((e) => e)
  assert.equal(take.code, 'readonly_unsupported')
  assert.equal(fake.requestsOf('import_agent_request').length, 0)
})

test("完成未看 follows Paseo: seen there, seen here; a new turn to look at, unread again", async (t) => {
  const { fake, a, rows } = await setup(t)
  const status = () => rows().find((r) => r.session_id === SID)?.status
  assert.equal(status(), 'done', 'a Paseo without the flag: done after a turn')
  fake.update(a.claude.id, { requiresAttention: false, attentionReason: null })
  await until('seen in Paseo', () => status() === 'idle')
  fake.update(a.claude.id, { requiresAttention: true, attentionReason: 'finished' })
  await until('a turn to look at', () => status() === 'done')
})

test('an agent Paseo unloads stays Unread, from when it finished, until it is looked at; a delegated one stays quiet (#8133)', async (t) => {
  const { fake, a, bridge, rows } = await setup(t)
  const row = (id) => rows().find((r) => r.session_id === id)
  const kid = agentSnapshot({ provider: 'my-codex', cwd: '/tmp', title: 'review', status: 'idle', labels: { 'paseo.parent-agent-id': a.claude.id } })
  fake.upsert(kid, PROJECT)
  fake.update(a.claude.id, { requiresAttention: true, attentionReason: 'finished' })
  await until('unread', () => row(SID)?.status === 'done' && row(`paseo-${kid.id}`))
  const at = row(SID).updated_at
  // Paseo lets an idle agent's process go after a while: closed, its attention mark and its time as they were.
  const unload = (id) => fake.upsert({ ...fake.agents.get(id).agent, status: 'closed' })
  unload(a.claude.id)
  await until('the unload seen', () => bridge.state.agents.get(a.claude.id)?.agent.status === 'closed')
  await new Promise((r) => setTimeout(r, 50))
  assert.deepEqual([row(SID).status, row(SID).updated_at], ['done', at], 'still Unread, from when it finished')
  fake.upsert({ ...fake.agents.get(kid.id).agent, requiresAttention: true, attentionReason: 'finished' })
  unload(kid.id)
  await until('the delegated agent unloaded', () => bridge.state.agents.get(kid.id)?.agent.status === 'closed')
  await until('not running', () => row(`paseo-${kid.id}`)?.status === 'offline')
  assert.equal(row(`paseo-${kid.id}`).is_subagent, true, "its creator's subtask: nobody waits for it, as in Paseo")
  // Looked at (Paseo's mark cleared, here or in Paseo's app): not running.
  fake.upsert({ ...fake.agents.get(a.claude.id).agent, requiresAttention: false, attentionReason: null, updatedAt: new Date(Date.now() + 5).toISOString() })
  await until('seen: not running', () => row(SID)?.status === 'offline')
})

test('continuing a Claude session: the agent Paseo has, else Paseo imports it under the same id', async (t) => {
  const { fake, node, a } = await setup(t)
  const mine = await call(node, 'session.start', { cwd: '/tmp', resume: SID, open_elsewhere: true })
  assert.equal(mine.session.session_id, SID, "Paseo's own agent: taken as it is, even with its claude running")
  assert.equal(fake.requestsOf('import_agent_request').length, 0)
  const other = 'cccccccc-0000-4000-8000-000000000009'
  const res = await call(node, 'session.start', { cwd: '/tmp', resume: other })
  const imp = fake.requestsOf('import_agent_request')[0]
  assert.deepEqual([imp.provider, imp.sessionId, imp.cwd, imp.labels['persona.session']], [a.claude.provider, other, '/tmp', other])
  assert.equal(res.session.session_id, other)
  assert.equal(res.session.transcript, other)
  const err = await call(node, 'session.start', { cwd: '/tmp', resume: 'dddddddd-0000-4000-8000-000000000004', open_elsewhere: true }).catch((e) => e)
  assert.equal(err.code, 'open_elsewhere', 'a claude Paseo does not run has it: no second writer')
})

// ---- starts and sends that fail late or across a dropped connection (#8130) ----

/** Paseo's answer to a request, written by a test's handler when it chooses (the handler returned null). */
const answerWith = (c, type, payload) => c.write(JSON.stringify({ type: 'session', message: { type, payload } }))

test('a message Paseo refuses after the wait: the phone was answered, the log says it (#8130)', async (t) => {
  const { fake, node, logs } = await setup(t)
  fake.handlers.send_agent_message_request = (msg, c) => {
    setTimeout(() => answerWith(c, 'send_agent_message_response', { requestId: msg.requestId, agentId: msg.agentId, accepted: false,
      error: 'provider failed to start' }), 1800) // past the bridge's wait (callWait 1500 in setup)
    return null
  }
  assert.deepEqual(await call(node, 'session.send', { session_id: SID, text: 'hi', client_msg_id: 'cm-late' }), { ok: true })
  await until('the refusal in the log', () => logs.some((l) => /refused the message to .* after the wait: provider failed to start/.test(l)))
})

test('a start Paseo refuses after the wait: starting meanwhile, then not running with why; a message to it is refused (#8130)', async (t) => {
  const { fake, node, logs, rows } = await setup(t)
  let refuse
  fake.handlers['agent.create.request'] = (msg, c) => {
    refuse = () => answerWith(c, 'agent.create.response', { requestId: msg.requestId, agent: null, error: 'Provider my-codex failed to start' })
    return null
  }
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'hi', launcher: 'my-codex' })
  const sid = res.session.session_id
  assert.deepEqual([res.session.status, res.session.starting, res.session.started_by_persona], ['idle', true, true], JSON.stringify(res.session))
  const row = () => rows().find((r) => r.session_id === sid)
  await until('listed while Paseo creates it', () => row()?.starting === true)
  assert.deepEqual((await call(node, 'session.items', { session_id: sid, limit: 50 })).items, [], 'its chat is empty, not an error')
  refuse()
  await until('its row says why', () => row()?.start_error)
  assert.deepEqual([row().status, row().starting], ['offline', undefined])
  assert.match(row().start_error, /Provider my-codex failed to start/)
  assert.ok(logs.some((l) => /starting .*: failed after the start was answered: Provider my-codex failed to start/.test(l)), logs.join('\n'))
  await assert.rejects(call(node, 'session.send', { session_id: sid, text: 'x', client_msg_id: 'cm-x' }), (err) => err.code === 'start_failed')
  assert.equal(fake.requestsOf('send_agent_message_request').length, 0, 'nothing sent')
  await call(node, 'session.close', { session_id: sid })
  await until('closed: its row gone', () => !row())
})

test("a start answered before Paseo was done: the agent's row takes over under the same id; a message and a watch wait for it (#8130)", async (t) => {
  const { fake, node, rows } = await setup(t)
  let finish
  fake.handlers['agent.create.request'] = (msg, c) => {
    finish = () => fake.answer(c, msg)
    return null
  }
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'hi', launcher: 'my-claude' })
  const sid = res.session.session_id
  const req = fake.requestsOf('agent.create.request')[0]
  assert.equal(sid, `paseo-${req.agentId}`)
  await call(node, 'session.watch', { session_id: sid, since_seq: 0 }) // the phone opens the new chat
  const sending = call(node, 'session.send', { session_id: sid, text: 'and also', client_msg_id: 'cm-early' })
  await sleep(200)
  assert.equal(fake.requestsOf('send_agent_message_request').length, 0, 'not before Paseo has the agent')
  finish()
  assert.deepEqual(await sending, { ok: true }, 'delivered within the wait')
  const sent = fake.requestsOf('send_agent_message_request')
  assert.deepEqual(sent.map((m) => [m.agentId, m.text]), [[req.agentId, 'and also']])
  await until("the agent's own row", () => rows().find((r) => r.session_id === sid && !r.starting && r.status === 'working'))
  assert.equal(rows().filter((r) => r.session_id === sid).length, 1)
  await until('its timeline followed', () => fake.requestsOf('agent.timeline.set_subscription.request').some((m) => m.agentIds?.[0] === req.agentId))
})

test("a start whose answer the dropped connection lost: once Paseo is back, the agent it made is the session, not a second one (#8130)", async (t) => {
  const { fake, node, rows } = await setup(t, { awayGrace: 5000 })
  fake.handlers['agent.create.request'] = (msg) => {
    fake.createAgent(msg) // Paseo makes it…
    setTimeout(() => fake.drop(), 10) // …and the connection goes before its answer does
    return null
  }
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'hi', launcher: 'my-claude' })
  const [req, ...more] = fake.requestsOf('agent.create.request')
  assert.equal(more.length, 0, "found in Paseo's directory once it was back: not asked again")
  assert.equal(res.session.session_id, `paseo-${req.agentId}`)
  assert.equal(res.session.starting, undefined, "the agent's own row: the start went through")
  await until('listed', () => rows().some((r) => r.session_id === res.session.session_id))
  assert.deepEqual([...fake.agents.values()].filter((e) => e.agent.labels['persona.session'] === res.session.session_id).map((e) => e.agent.id), [req.agentId])
})

test('a start whose request the dropped connection cut off: asked again with the same idempotency key, one agent (#8130)', async (t) => {
  const { fake, node, rows } = await setup(t, { awayGrace: 5000 })
  let first = true
  fake.handlers['agent.create.request'] = () => {
    if (!first) return undefined // asked again: Paseo makes it
    first = false
    setTimeout(() => fake.drop(), 10) // the connection goes before Paseo did anything with it
    return null
  }
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'hi', launcher: 'my-claude' })
  const [a, b, ...more] = fake.requestsOf('agent.create.request')
  assert.equal(more.length, 0)
  assert.equal(a.idempotencyKey, `persona:${a.labels['persona.session']}:1`)
  assert.deepEqual({ ...b, requestId: undefined }, { ...a, requestId: undefined }, 'the same request: its agent id and its idempotency key')
  assert.equal(res.session.session_id, a.labels['persona.session'])
  await until('listed', () => rows().some((r) => r.session_id === res.session.session_id && !r.starting))
  assert.equal([...fake.agents.values()].filter((e) => e.agent.labels['persona.session'] === res.session.session_id).length, 1)
})

test("the fake daemon keeps keyed creations as Paseo 0.15 does: the same request gets the same agent, another one under the key is refused", async (t) => {
  const fake = await startFakePaseo(t, { providers: PROVIDERS, entries: ENTRIES })
  const msg = { type: 'agent.create.request', requestId: 'r1', agentId: 'aaaaaaaa-0000-4000-8000-0000000000aa', idempotencyKey: 'k1',
    config: { provider: 'my-claude', cwd: '/tmp' }, labels: { 'persona.session': 's1' } }
  const made = fake.createAgent(msg)
  assert.deepEqual(fake.createAgent({ ...msg, requestId: 'r2' }), made, 'asked again: the agent it made')
  assert.equal(fake.agents.size, 1)
  assert.deepEqual(fake.createAgent({ ...msg, requestId: 'r3', initialPrompt: 'other' }), { agent: null, error: 'agent_request_key_conflict' })
})

test("the session id the hub names for a start is the session's: label, idempotency key, tools, row; one taken or malformed is not (#8130)", async (t) => {
  const dir = mkdtempSync('/tmp/ppaseo-mcp-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const mcpConfig = join(dir, 'mcp.json')
  writeFileSync(mcpConfig, JSON.stringify(MCP_JSON))
  const { fake, node, logs, rows } = await setup(t, { mcpConfig })
  const named = 'bbbbbbbb-0000-4000-8000-000000000042'
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-claude', session_id: named })
  const req = fake.requestsOf('agent.create.request')[0]
  assert.deepEqual([req.labels['persona.session'], req.idempotencyKey, req.config.mcpServers.persona.env.PERSONA_SESSION_ID, res.session.session_id],
    [named, `persona:${named}:1`, named, named])
  await until('listed under it, as one Persona started', () => rows().find((r) => r.session_id === named)?.started_by_persona === true)
  // Another session's id (a listed agent's), or none the node takes: the start picks its own.
  const taken = await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-claude', session_id: SID })
  assert.match(taken.session.session_id, /^paseo-/)
  assert.ok(logs.some((l) => l.includes(`session ${SID} is someone else's already`)))
  const bad = await call(node, 'session.start', { cwd: '/tmp', prompt: 'x', launcher: 'my-claude', session_id: '../no' })
  assert.match(bad.session.session_id, /^paseo-/)
  // Rows say which agents Persona started: one with its label, not one made in Paseo's own app.
  assert.equal(rows().find((r) => r.session_id === 'paseo-e1')?.started_by_persona, true)
  assert.equal(rows().find((r) => r.session_id === SID)?.started_by_persona, undefined)
})

test('a takeover Paseo takes long to import: listed as starting; the message waits for it and goes to it once Paseo is done (#8130)', async (t) => {
  const { fake, node, rows } = await setup(t)
  let finish
  fake.handlers.import_agent_request = (msg, c) => {
    finish = () => fake.answer(c, msg)
    return null
  }
  const other = 'cccccccc-0000-4000-8000-000000000009'
  const res = await call(node, 'session.start', { cwd: '/tmp', resume: other })
  assert.deepEqual([res.session.session_id, res.session.starting, res.session.transcript, res.session.started_by_persona], [other, true, other, true])
  const row = () => rows().find((r) => r.session_id === other)
  await until('listed as starting', () => row()?.starting === true)
  // A second takeover meanwhile is the same one.
  assert.equal((await call(node, 'session.start', { cwd: '/tmp', resume: other })).session.session_id, other)
  assert.equal(fake.requestsOf('import_agent_request').length, 1, 'one import')
  assert.deepEqual(await call(node, 'session.send', { session_id: other, text: 'carry on', client_msg_id: 'cm-take' }), { ok: true, queued: true })
  assert.equal(fake.requestsOf('send_agent_message_request').length, 0, 'not before Paseo has the agent')
  finish()
  await until('delivered once imported', () => fake.requestsOf('send_agent_message_request').length === 1)
  const agent = [...fake.agents.values()].find((e) => e.agent.persistence?.sessionId === other)?.agent
  assert.deepEqual(fake.requestsOf('send_agent_message_request').map((m) => [m.agentId, m.text]), [[agent.id, 'carry on']])
  await until("the agent's own row", () => row() && !row().starting)
})

test('a takeover Paseo fails after the wait: its row says why, the message that waited is not sent, the next one takes it over again (#8130)', async (t) => {
  const { fake, node, rows } = await setup(t)
  let fail
  fake.handlers.import_agent_request = (msg, c) => {
    if (fail) return undefined // taken over again: Paseo imports it
    fail = () => answerWith(c, 'status', { status: 'agent_create_failed', requestId: msg.requestId, error: 'Import requires cwd from the selected provider session' })
    return null
  }
  const other = 'cccccccc-0000-4000-8000-000000000009'
  await call(node, 'session.start', { cwd: '/tmp', resume: other })
  assert.deepEqual(await call(node, 'session.send', { session_id: other, text: 'carry on', client_msg_id: 'cm-take' }), { ok: true, queued: true })
  fail()
  const row = () => rows().find((r) => r.session_id === other)
  await until('its row says why', () => row()?.start_error)
  assert.match(row().start_error, /Import requires cwd/)
  await sleep(100)
  assert.equal(fake.requestsOf('send_agent_message_request').length, 0)
  // Still the user's conversation: the next message takes it over again, and goes to it.
  assert.deepEqual(await call(node, 'session.send', { session_id: other, text: 'once more', client_msg_id: 'cm-again' }), { ok: true })
  assert.equal(fake.requestsOf('import_agent_request').length, 2)
  const agent = [...fake.agents.values()].find((e) => e.agent.persistence?.sessionId === other)?.agent
  assert.deepEqual(fake.requestsOf('send_agent_message_request').map((m) => [m.agentId, m.text]), [[agent.id, 'once more']])
  await until("the agent's own row", () => row() && !row().start_error && !row().starting)
})

test('a creation Paseo never answers is given up after a while: its row says so (#8130)', async (t) => {
  const { fake, node, rows } = await setup(t, { startGiveUp: 2500 })
  fake.handlers['agent.create.request'] = () => null // never answered, the connection up all along
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'hi', launcher: 'my-codex' })
  const row = () => rows().find((r) => r.session_id === res.session.session_id)
  await until('given up', () => row()?.start_error, 6000)
  assert.deepEqual([row().status, row().starting], ['offline', undefined])
  assert.match(row().start_error, /agent\.create\.request/)
})

test('a start that failed late, whose agent Paseo makes after all: the agent is the session, and nothing of the failure stays (#8130)', async (t) => {
  const { fake, node, bridge, rows } = await setup(t)
  let refuse
  fake.handlers['agent.create.request'] = (msg, c) => {
    refuse = () => answerWith(c, 'agent.create.response', { requestId: msg.requestId, agent: null, error: 'agent_request_outcome_unknown' })
    return null
  }
  const res = await call(node, 'session.start', { cwd: '/tmp', prompt: 'hi', launcher: 'my-codex' })
  const sid = res.session.session_id
  refuse()
  await until('its row says why', () => rows().find((r) => r.session_id === sid)?.start_error)
  fake.upsert(agentSnapshot({ id: sid.slice('paseo-'.length), provider: 'my-codex', cwd: '/tmp', status: 'running', labels: { 'persona.session': sid } }), PROJECT)
  await until("the agent's row", () => rows().find((r) => r.session_id === sid && !r.start_error && r.status === 'working'))
  assert.equal(bridge.state.inFlight.has(sid), false, 'the failure is not kept behind the agent')
})

test("a project's worktrees are one space; a new session at a worktree goes into Paseo's workspace there", async (t) => {
  const dir = realpathSync(mkdtempSync('/tmp/ppaseo-wt-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const app = join(dir, 'app')
  const wt = join(dir, 'brave-fox')
  mkdirSync(app)
  mkdirSync(wt)
  const key = 'prj_app' // the placement's projectKey: Paseo's project record id
  const checkout = (cwd, o = {}) => ({ cwd, isGit: true, currentBranch: null, remoteUrl: null, worktreeRoot: cwd,
    isPaseoOwnedWorktree: o.paseo ?? false, mainRepoRoot: o.main ?? null })
  const placed = [
    { agent: agentSnapshot({ provider: 'my-codex', cwd: app, workspaceId: 'wks_main', title: 'M', updatedAt: '2026-11-01T00:00:00Z' }),
      project: { projectKey: key, projectName: 'app', workspaceName: '主线', checkout: checkout(app) } },
    { agent: agentSnapshot({ provider: 'my-codex', cwd: wt, workspaceId: 'wks_wt', title: 'W', updatedAt: '2026-11-02T00:00:00Z' }),
      project: { projectKey: key, projectName: 'app', workspaceName: '修登录', checkout: checkout(wt, { paseo: true, main: app }) } },
    // A later record Paseo made when handed the worktree's directory alone (#2802): a project of its own, named after the folder.
    { agent: agentSnapshot({ provider: 'my-codex', cwd: wt, workspaceId: 'wks_stray', title: 'S', updatedAt: '2026-11-03T00:00:00Z' }),
      project: { projectKey: 'prj_stray', projectName: 'brave-fox', workspaceName: null, checkout: checkout(wt, { paseo: true, main: app }) } },
  ]
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  const { fake, node, rows } = await setup(t, { agents: [...base, ...placed] })
  await until('the worktree agents', () => ['M', 'W', 'S'].every((title) => rows().some((r) => r.title === title)))
  const byTitle = Object.fromEntries(rows().filter((r) => r.space_id === key).map((r) => [r.title, r]))
  assert.deepEqual(['M', 'W', 'S'].map((x) => [byTitle[x].space_name, byTitle[x].slot_id, byTitle[x].slot_name]),
    [['app', undefined, '主线'], ['app', undefined, '修登录'], ['app', undefined, 'brave-fox']],
    "one space named after the repository's project, the stray record's too; each names its workspace")
  assert.deepEqual(['M', 'W', 'S'].map((x) => byTitle[x].repo_root), [app, app, app], "按仓库: the repository's main checkout (#2840)")

  const started = (params) => call(node, 'session.start', { prompt: 'x', launcher: 'my-codex', ...params })
    .then(() => fake.requestsOf('agent.create.request').at(-1))
  assert.equal((await started({ cwd: wt, workspace_id: key })).workspaceId, 'wks_wt', "the project's space and a worktree: its workspace")
  assert.equal((await started({ cwd: wt })).workspaceId, 'wks_wt', 'a worktree alone: its workspace under the repository, not the stray record')
  assert.equal((await started({ cwd: app, workspace_id: key })).workspaceId, undefined, 'the checkout: a new workspace of the project')
  assert.equal((await started({ cwd: app, workspace_id: 'wks_main' })).workspaceId, 'wks_main', 'a workspace named (older apps): that one')
  const other = 'cccccccc-0000-4000-8000-00000000000a'
  await call(node, 'session.start', { cwd: wt, resume: other })
  assert.equal(fake.requestsOf('import_agent_request').at(-1).workspaceId, 'wks_wt', 'continuing a session at a worktree: its workspace')
  // Paseo slow to answer (its provider starting): the row it will have is in its project already.
  fake.handlers['agent.create.request'] = () => null
  const slow = (params) => call(node, 'session.start', { prompt: 'x', launcher: 'my-codex', ...params }).then((r) => r.session)
  const inWt = await slow({ cwd: wt })
  assert.deepEqual([inWt.space_id, inWt.space_name, inWt.slot_id, inWt.slot_name], [key, 'app', undefined, '修登录'])
  const inApp = await slow({ cwd: app, workspace_id: key })
  assert.deepEqual([inApp.space_id, inApp.space_name, inApp.slot_name], [key, 'app', undefined], 'a new workspace of the project: no name yet')
})

test("an agent that leaves Paseo's list has its Claude session named to the node, until it is listed again", async (t) => {
  const day = 86_400_000
  const past = (id, days, o = {}) => ({ agent: agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: id, status: 'closed',
    archivedAt: new Date(Date.now() - days * day).toISOString(), updatedAt: new Date(Date.now() - days * day).toISOString(),
    persistence: { provider: 'my-claude', sessionId: id, metadata: { provider: 'claude' } }, ...o }), project: PROJECT })
  const RECENT = 'bbbbbbbb-0000-4000-8000-000000000001'
  const OLD = 'bbbbbbbb-0000-4000-8000-000000000002'
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  // Archived while the bridge was not running: one lately, one long ago, one of Codex (no transcript of Claude's).
  const { fake, node } = await setup(t, { agents: [...base, past(RECENT, 1), past(OLD, 30),
    past('th-9', 1, { provider: 'my-codex', persistence: { provider: 'my-codex', sessionId: 'th-9', metadata: { provider: 'codex' } } })] })
  const pushed = () => node.last('runtime.sessions')
  await until('the ones archived before', () => pushed().unlisted_transcripts)
  assert.deepEqual(pushed().unlisted_transcripts, [RECENT], 'archived lately: named; long ago or no Claude session: not')
  assert.equal(pushed().sessions.some((r) => r.title === RECENT), false, 'an archived agent is no row')
  assert.equal(fake.requestsOf('fetch_agents_request').filter((r) => r.filter?.includeArchived).length, 1, 'one page: all of them fit')

  // Closed from the phone: archived in Paseo, gone from the rows, its session named.
  await call(node, 'session.close', { session_id: SID })
  await until('named', () => pushed().unlisted_transcripts?.includes(SID))
  assert.equal(pushed().sessions.some((r) => r.session_id === SID), false)
  const codex = pushed().sessions.find((r) => r.title === 'B').session_id
  await call(node, 'session.close', { session_id: codex })
  await until('the Codex agent gone', () => !pushed().sessions.some((r) => r.session_id === codex))
  assert.deepEqual([...pushed().unlisted_transcripts].sort(), [SID, RECENT].sort(), 'an agent without a Claude session names nothing')

  // Taken over again (a message to the session not running): Paseo lists it, under another agent; named no more.
  await call(node, 'session.start', { cwd: '/tmp', resume: SID })
  await until('listed again', () => pushed().sessions.some((r) => r.session_id === SID))
  await until('named no more', () => !pushed().unlisted_transcripts?.includes(SID))
  assert.deepEqual(pushed().unlisted_transcripts, [RECENT])
})

test("only an archived agent of the user's own is named: not a deleted one, nor an agent another agent created", async (t) => {
  const { fake, node, a } = await setup(t)
  const pushed = () => node.last('runtime.sessions')
  const KID = 'cccccccc-0000-4000-8000-000000000001'
  fake.upsert(agentSnapshot({ id: 'kid', provider: 'my-claude', cwd: '/tmp', title: 'KID', labels: { 'paseo.parent-agent-id': a.claude.id },
    persistence: { provider: 'my-claude', sessionId: KID, metadata: { provider: 'claude' } } }), PROJECT)
  await until('the delegated agent', () => pushed().sessions.some((r) => r.title === 'KID'))
  // Deleted, and the list told of the removal, which says what an archive's does. Asked, Paseo has no such agent: it
  // stays gone.
  fake.delete(a.labeled.id, { told: true })
  await until('asked what became of it', () => fake.requestsOf('fetch_agent_request').some((r) => r.agentId === a.labeled.id))
  await until('off the list', () => !pushed().sessions.some((r) => r.title === 'E'))
  // A removal of an agent that was never listed (Paseo sends one for each change of its internal agents) asks nothing.
  for (let i = 0; i < 3; i++) fake.emit('agents', { type: 'agent_update', payload: { kind: 'remove', agentId: 'internal-1' } })
  // The orchestrator archived in Paseo's own app: the agent it created is archived with it, and only its own session is named.
  fake.archive(a.claude.id)
  await until('named', () => pushed().unlisted_transcripts?.includes(SID))
  await until('asked about both', () => ['kid', a.claude.id].every((id) => fake.requestsOf('fetch_agent_request').some((r) => r.agentId === id)))
  await new Promise((r) => setTimeout(r, 50))
  assert.deepEqual(pushed().unlisted_transcripts, [SID], 'not the deleted agent\'s (SID2), nor the delegated one\'s')
  assert.equal(pushed().sessions.some((r) => r.title === 'KID' || r.title === 'A'), false)
  assert.equal(fake.requestsOf('fetch_agent_request').some((r) => r.agentId === 'internal-1'), false)
  assert.equal(fake.requestsOf('fetch_agent_request').filter((r) => r.agentId === a.claude.id).length, 1, 'asked once, however often the removal is told')
})

test('an agent deleted in another client leaves the list: one that ran once Paseo no longer has it, though Paseo only says it closed', async (t) => {
  const { fake, a, rows } = await setup(t, { closedChecks: [30, 200] })
  const asked = (id) => fake.requestsOf('fetch_agent_request').filter((r) => r.agentId === id).length
  const has = (agent) => rows().some((r) => r.session_id === `paseo-${agent.id}`)
  // Paseo closes the agent, and removes its record a moment later: asked before that, it still has it.
  fake.delete(a.codex.id, { after: 100 })
  await until('seen closed', () => rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.status === 'offline')
  await until('asked, and Paseo still had it', () => asked(a.codex.id) === 1)
  assert.equal(has(a.codex), true, 'an agent Paseo still has stays')
  await until('asked again: gone', () => !has(a.codex))
  assert.equal(asked(a.codex.id), 2)
  // Its last state comes once more, late (Paseo sends an agent's updates one after another, each when it is ready).
  fake.emit('agents', { type: 'agent_update', payload: { kind: 'upsert', agent: { ...a.codex, status: 'closed' }, project: PROJECT } })
  fake.upsert(agentSnapshot({ provider: 'my-codex', cwd: '/tmp', title: 'LATER' }), PROJECT)
  await until('a later change listed', () => rows().some((r) => r.title === 'LATER'))
  assert.equal(has(a.codex), false, 'an update that comes after it was deleted does not list it again')
  // An agent only stopped is asked for too, and stays.
  fake.update(a.claude.id, { status: 'closed' })
  await until('asked twice', () => asked(a.claude.id) === 2)
  assert.equal(rows().find((r) => r.session_id === SID)?.status, 'offline')
  // A new connection reads the directory afresh: an agent that is in it is listed, whatever was said of its id before.
  fake.agents.set(a.codex.id, { agent: { ...a.codex, status: 'idle' }, project: PROJECT })
  fake.drop()
  await until('listed again', () => has(a.codex))
})

test('the directory is read again now and then: an agent deleted without a word leaves the list, and an archived one is named no more', async (t) => {
  const { fake, node, a, rows } = await setup(t, { sweepMs: 40 })
  const pushed = () => node.last('runtime.sessions')
  const asked = (id) => fake.requestsOf('fetch_agent_request').filter((r) => r.agentId === id).length
  // One that was not running (Paseo says nothing at all), and one archived before: its Claude session was named.
  fake.archive(a.claude.id)
  await until('named', () => pushed().unlisted_transcripts?.includes(SID))
  fake.delete(a.grok.id)
  fake.delete(a.claude.id)
  await until('off the list', () => !rows().some((r) => r.session_id === `paseo-${a.grok.id}`))
  await until('named no more', () => !pushed().unlisted_transcripts?.includes(SID))
  // An archive the list was never told of: found out the same way, and its Claude session is named.
  const LOST = 'eeeeeeee-0000-4000-8000-000000000001'
  const lost = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'LOST', persistence: { provider: 'my-claude', sessionId: LOST, metadata: { provider: 'claude' } } })
  fake.upsert(lost, PROJECT)
  await until('listed', () => rows().some((r) => r.session_id === LOST))
  const at = new Date().toISOString()
  fake.agents.get(lost.id).agent = { ...lost, status: 'closed', archivedAt: at, updatedAt: at }
  await until('off the list, and named', () => !rows().some((r) => r.session_id === LOST) && pushed().unlisted_transcripts?.includes(LOST))
  // An agent a reading of the directory missed (it moved from one page to another between the two) is asked for: it stays.
  fake.handlers.fetch_agents_request = (msg) => {
    if (msg.subscribe || msg.filter?.includeArchived) return undefined
    const entries = [...fake.agents.values()].filter((e) => !e.agent.archivedAt && e.agent.id !== a.labeled.id)
    return { payload: { subscriptionId: null, entries, pageInfo: { nextCursor: null, prevCursor: null, hasMore: false } } }
  }
  await until('asked for it', () => asked(a.labeled.id) >= 2)
  assert.equal(rows().some((r) => r.session_id === 'paseo-e1'), true)
  assert.equal(asked(a.codex.id), 0, 'an agent the directory lists is not asked for')
})

test('agents archived while the bridge was away are read a page at a time, newest first, as far back as their sessions are named', async (t) => {
  const day = 86_400_000
  const id = (n) => `dddddddd-0000-4000-8000-${String(n).padStart(12, '0')}`
  const past = (n, days) => ({ agent: agentSnapshot({ id: `gone-${n}`, provider: 'my-claude', cwd: '/tmp', title: `GONE${n}`, status: 'closed',
    archivedAt: new Date(Date.now() - days * day).toISOString(), updatedAt: new Date(Date.now() - days * day).toISOString(),
    persistence: { provider: 'my-claude', sessionId: id(n), metadata: { provider: 'claude' } } }), project: PROJECT })
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  // Paseo's directory with the archived ones included, newest first, four to a page: the five listed agents, three
  // archived this week, then older and older ones.
  const { fake, node } = await setup(t, { pageSize: 4, agents: [...base, past(1, 1), past(2, 2), past(3, 3), ...[4, 5, 6, 7, 8, 9, 10].map((n) => past(n, n * 10))] })
  await until('the ones archived lately', () => node.last('runtime.sessions').unlisted_transcripts?.length === 3)
  assert.deepEqual([...node.last('runtime.sessions').unlisted_transcripts].sort(), [id(1), id(2), id(3)])
  const pages = fake.requestsOf('fetch_agents_request').filter((r) => r.filter?.includeArchived)
  assert.deepEqual(pages.map((r) => r.page.cursor), [undefined, '4', '8'], 'the page that ends before the week began is the last one read')
  assert.ok(pages.every((r) => r.sort?.[0]?.key === 'updated_at' && r.sort[0].direction === 'desc'))
})

test('a session Persona started keeps its id once archived: a row not running, its transcript the content; a message takes the agent back under that id (#4771)', async (t) => {
  const { fake, node, a, rows } = await setup(t)
  const pushed = () => node.last('runtime.sessions')
  const row = () => rows().find((r) => r.session_id === 'paseo-e1')
  await until('the archive read', () => pushed().unlisted_transcripts)
  await call(node, 'session.close', { session_id: 'paseo-e1' })
  assert.equal(fake.requestsOf('archive_agent_request').at(-1).agentId, a.labeled.id)
  await until('not running', () => row()?.status === 'offline')
  assert.deepEqual([row().transcript, row().title, row().space_id, row().slot_name, row().agent, row().launcher],
    [SID2, 'E', 'prj_1', '主线', 'claude', 'my-claude'], 'the same row in the same place, its transcript the conversation')
  assert.equal(rows().some((r) => r.session_id === SID2), false, "no row under its transcript's id")
  assert.equal(pushed().unlisted_transcripts.includes(SID2), false, 'nor is its transcript named: the row names it')
  // Archived already: nothing runs, its modes stay as they are, and closing it again closes nothing more.
  const n = (type) => fake.requestsOf(type).length
  const before = [n('cancel_agent_request'), n('archive_agent_request')]
  await call(node, 'session.interrupt', { session_id: 'paseo-e1' })
  await call(node, 'session.close', { session_id: 'paseo-e1' })
  assert.deepEqual([n('cancel_agent_request'), n('archive_agent_request')], before)
  const refused = await call(node, 'session.modes', { session_id: 'paseo-e1', permission_mode: 'plan' }).catch((e) => e)
  assert.equal(refused.code, 'not_running')
  assert.equal(n('set_agent_mode_request'), 0, 'Paseo is not asked: it refuses an archived agent')
  // Renamed: Paseo renames its record and tells no list; the row takes the name all the same.
  await call(node, 'session.rename', { session_id: 'paseo-e1', title: '改过' })
  assert.equal(fake.requestsOf('update_agent_request').at(-1).agentId, a.labeled.id)
  await until('renamed', () => row()?.title === '改过')
  // Watched while archived: its agent is followed, and a message takes it back from the archive under the same id.
  await call(node, 'session.watch', { session_id: 'paseo-e1', since_seq: 0, transcript: true })
  await until('followed', () => [...fake.subs.values()].some((s) => s.kind === 'timeline' && s.agentIds[0] === a.labeled.id))
  await call(node, 'session.send', { session_id: 'paseo-e1', text: '接着来', client_msg_id: 'cm-back' })
  assert.equal(fake.requestsOf('send_agent_message_request').at(-1).agentId, a.labeled.id)
  await until('running again', () => row()?.status === 'working')
  assert.equal(fake.agents.get(a.labeled.id).agent.labels['persona.session'], 'paseo-e1', 'its label as it was')
  fake.stream(a.labeled.id, { type: 'timeline', provider: 'claude', turnId: 't', item: { type: 'assistant_message', text: '在', messageId: 'm' } }, 5)
  await until('its live text', () => node.all('session.delta').some((d) => d.session_id === 'paseo-e1' && d.text === '在'))
  assert.equal(rows().filter((r) => r.transcript === SID2).length, 1, 'one row for the conversation')
  assert.equal(node.all('runtime.sessions').some((p) => p.sessions.some((r) => r.session_id === SID2) || p.unlisted_transcripts?.includes(SID2)), false,
    'never under its transcript id, in no list')
})

test('a Persona-started Codex session keeps its id and thread through archive, bridge restart and a new message (#5129)', async (t) => {
  const thread = '0199aaaa-bbbb-7ccc-8ddd-eeeeffff0003'
  const { fake, node, bridge, paseo, rows, a } = await setup(t)
  const started = await call(node, 'session.start', { cwd: '/tmp', prompt: 'Check the synthetic project', launcher: 'my-codex' })
  const sid = started.session.session_id
  const id = fake.requestsOf('agent.create.request').at(-1).agentId
  fake.update(id, { persistence: { provider: 'my-codex', sessionId: thread, metadata: { provider: 'codex', threadId: thread } } })
  const row = () => rows().find((r) => r.session_id === sid)
  await until('the Codex thread known', () => row()?.codex_thread === thread)
  await call(node, 'session.close', { session_id: sid })
  await until('the archived Codex session still listed', () => row()?.status === 'offline')
  assert.deepEqual([row().session_id, row().codex_thread, row().agent, row().launcher], [sid, thread, 'codex', 'my-codex'])
  assert.equal(fake.agents.get(id).agent.labels['persona.session'], sid)

  const second = await call(node, 'session.start', { cwd: '/tmp', prompt: 'Check another synthetic project', launcher: 'my-codex' })
  const secondId = fake.requestsOf('agent.create.request').at(-1).agentId
  const secondThread = '0199aaaa-bbbb-7ccc-8ddd-eeeeffff0005'
  fake.update(secondId, { persistence: { provider: 'my-codex', sessionId: secondThread } })
  await until('the second thread known', () => rows().some((r) => r.session_id === second.session.session_id && r.codex_thread === secondThread))
  await call(node, 'session.close', { session_id: second.session.session_id })
  await until('both archived Codex conversations listed', () => rows().filter((r) => r.status === 'offline' && [thread, secondThread].includes(r.codex_thread)).length === 2)
  assert.ok(node.last('runtime.sessions').unlisted_transcripts.every((x) => x && ![thread, secondThread].includes(x)), 'Codex threads never enter the Claude transcript list')

  // An agent started in Paseo itself still leaves the runtime list when archived, even with a valid thread id.
  fake.update(a.codex.id, { persistence: { provider: 'my-codex', sessionId: '0199aaaa-bbbb-7ccc-8ddd-eeeeffff0004' } })
  await call(node, 'session.close', { session_id: `paseo-${a.codex.id}` })
  await until('the Paseo-originated Codex agent gone', () => !rows().some((r) => r.session_id === `paseo-${a.codex.id}`))

  bridge.stop()
  const again = createPaseoBridge({ paseo: new PaseoClient({ home: paseo.home, retryMin: 30, retryMax: 100 }) },
    { socketPath: node.path, retryMs: 30, pushDelayMs: 5, refreshMs: 0, callWait: 1500, sweepMs: 0 })
  const mark = node.events.length
  again.start()
  t.after(() => again.stop())
  const fresh = () => node.events.slice(mark).filter((e) => e.event === 'runtime.sessions').at(-1)?.params.sessions ?? []
  await until('the archived Codex row read after reconnect', () => fresh().some((r) => r.session_id === sid && r.status === 'offline' && r.codex_thread === thread))
  assert.ok(fresh().some((r) => r.session_id === second.session.session_id && r.codex_thread === secondThread))
  assert.equal(fresh().some((r) => r.session_id === `paseo-${a.codex.id}`), false)
  await call(node, 'session.send', { session_id: sid, text: 'Continue the synthetic check', client_msg_id: 'cm-codex-back' })
  assert.equal(fake.requestsOf('send_agent_message_request').at(-1).agentId, id)
  await until('the same Codex session restored', () => fresh().some((r) => r.session_id === sid && r.status === 'working' && r.codex_thread === thread))
  assert.equal(fake.agents.get(id).agent.archivedAt, null)
  assert.equal(fake.agents.get(id).agent.labels['persona.session'], sid)
  assert.equal(fake.requestsOf('agent.create.request').length, 2, 'restoring sends to the original agent instead of creating another')
  assert.equal(node.all('runtime.sessions').some((p) => p.sessions.some((r) => r.session_id === thread)), false, 'no push renames the session to its thread')
  assert.equal(fresh().filter((r) => r.codex_thread === thread).length, 1)

  const resumed = await call(node, 'session.start', { cwd: '/tmp', resume: second.session.session_id })
  assert.equal(fake.requestsOf('import_agent_request').at(-1).sessionId, secondThread)
  assert.equal(resumed.session.session_id, second.session.session_id)
  assert.equal(fake.agents.get(secondId).agent.archivedAt, null)
  assert.equal(fake.requestsOf('agent.create.request').length, 2)
})

for (const unavailable of ['disabled', 'missing']) test(`an archived Codex resume refuses its ${unavailable} provider instead of importing the thread through Claude (#5129)`, async (t) => {
  const thread = '0199aaaa-bbbb-7ccc-8ddd-eeeeffff0006'
  const kept = agentSnapshot({ id: 'kept-codex', provider: 'my-codex', labels: { 'persona.session': 'paseo-kept-codex' },
    archivedAt: new Date().toISOString(), persistence: { provider: 'my-codex', sessionId: thread, metadata: { provider: 'codex' } } })
  const entries = unavailable === 'missing' ? ENTRIES.filter((e) => e.provider !== 'my-codex')
    : ENTRIES.map((e) => e.provider === 'my-codex' ? { ...e, enabled: false } : e)
  const { fake, node, rows } = await setup(t, { entries,
    agents: [...Object.values(agents()).map((agent) => ({ agent, project: PROJECT })), { agent: kept, project: PROJECT }] })
  await until('the retained Codex row', () => rows().some((r) => r.session_id === 'paseo-kept-codex'))
  await assert.rejects(call(node, 'session.start', { cwd: '/tmp', resume: 'paseo-kept-codex' }), (e) => e.code === 'unavailable')
  assert.equal(fake.requestsOf('import_agent_request').length, 0, 'no provider gets an import for the unavailable Codex session')
  assert.ok(fake.agents.get(kept.id).agent.archivedAt)
})

test('archived Codex rows share a thread only once, and a live row takes precedence (#5129)', async (t) => {
  const thread = '0199aaaa-bbbb-7ccc-8ddd-eeeeffff0007'
  const kept = (id, ago) => ({ agent: agentSnapshot({ id, provider: 'my-codex', labels: { 'persona.session': `paseo-${id}` },
    archivedAt: new Date(Date.now() - ago).toISOString(), updatedAt: new Date(Date.now() - ago).toISOString(),
    persistence: { provider: 'my-codex', sessionId: thread } }), project: PROJECT })
  const old = kept('older-codex', 60_000), latest = kept('newer-codex', 1000)
  const { fake, rows } = await setup(t, {
    agents: [...Object.values(agents()).map((agent) => ({ agent, project: PROJECT })), old, latest],
  })
  await until('the newest archived Codex record', () => rows().some((r) => r.session_id === 'paseo-newer-codex'))
  assert.deepEqual(rows().filter((r) => r.codex_thread === thread).map((r) => r.session_id), ['paseo-newer-codex'])
  const live = agentSnapshot({ id: 'live-codex', provider: 'my-codex', status: 'running', persistence: { provider: 'my-codex', sessionId: thread } })
  fake.upsert(live, PROJECT)
  await until('the live Codex row', () => rows().some((r) => r.session_id === 'paseo-live-codex'))
  assert.deepEqual(rows().filter((r) => r.codex_thread === thread).map((r) => r.session_id), ['paseo-live-codex'])
})

test('continuing a session an archived agent keeps: Paseo imports it into that agent, its label as it was', async (t) => {
  const { fake, node, a, rows } = await setup(t)
  await call(node, 'session.close', { session_id: 'paseo-e1' })
  await until('not running', () => rows().find((r) => r.session_id === 'paseo-e1')?.status === 'offline')
  const plan = await call(node, 'session.start', { cwd: '/tmp', resume: 'paseo-e1', permission_mode: 'plan' }).catch((e) => e)
  assert.equal(plan.code, 'readonly_unsupported', 'a takeover keeps its own mode')
  const res = await call(node, 'session.start', { cwd: '/nowhere', resume: 'paseo-e1' })
  const imp = fake.requestsOf('import_agent_request').at(-1)
  assert.deepEqual([imp.provider, imp.sessionId, imp.cwd, imp.labels['persona.session']], ['my-claude', SID2, '/tmp', 'paseo-e1'],
    "its own provider, Claude session and directory: Paseo's record of it")
  assert.equal(res.session.session_id, 'paseo-e1')
  assert.deepEqual([fake.agents.get(a.labeled.id).agent.archivedAt, fake.agents.size], [null, 5], 'the same agent, back: no second one')
  await until('listed as running again', () => rows().find((r) => r.session_id === 'paseo-e1')?.status === 'idle')
  // Listed again, it is the agent Paseo has, whatever the session was called.
  const again = await call(node, 'session.start', { cwd: '/tmp', resume: 'paseo-e1' })
  assert.equal(again.session.session_id, 'paseo-e1')
  assert.equal(fake.requestsOf('import_agent_request').length, 1)
})

// Another Claude provider of the machine's, whose agents are not the latest: a takeover by "the latest" picks another.
const OLD_CLAUDE = {
  providers: { ...PROVIDERS, 'old-claude': { extends: 'claude', label: 'Old Claude' } },
  entries: [...ENTRIES, { provider: 'old-claude', enabled: true, status: 'ready', label: 'Old Claude', models: [] }],
}

test("continuing the Claude session of an archived agent named to the node: Paseo imports it into that agent, with its own provider and folder (#8131)", async (t) => {
  const T = 'bbbbbbbb-0000-4000-8000-0000000000a1'
  const day = 86_400_000
  // Started in Paseo's app on a provider other than the machine's latest, listed under its Claude session, archived.
  const old = agentSnapshot({ provider: 'old-claude', cwd: '/tmp', title: 'Fix the build', status: 'closed',
    archivedAt: new Date(Date.now() - day).toISOString(), updatedAt: new Date(Date.now() - day).toISOString(),
    persistence: { provider: 'old-claude', sessionId: T, metadata: { provider: 'claude' } } })
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  const { fake, node, rows } = await setup(t, { ...OLD_CLAUDE, agents: [...base, { agent: old, project: PROJECT }] })
  const pushed = () => node.last('runtime.sessions')
  await until('named to the node', () => pushed().unlisted_transcripts?.includes(T))
  const res = await call(node, 'session.start', { cwd: '/nowhere', resume: T })
  const imp = fake.requestsOf('import_agent_request').at(-1)
  assert.deepEqual([imp.provider, imp.sessionId, imp.cwd, imp.labels['persona.session']], ['old-claude', T, '/tmp', T],
    "its own provider and directory: Paseo's record of it")
  assert.equal(res.session.session_id, T)
  assert.deepEqual([fake.agents.get(old.id).agent.archivedAt, fake.agents.size], [null, 6], 'the same agent, back: no second one')
  await until('listed again', () => rows().some((r) => r.session_id === T))
  await until('named no more', () => !pushed().unlisted_transcripts?.includes(T))
})

test("two agents on one Claude session are one conversation: under the older one's id, the newer one gets the messages, unless the older one is at work (#8131)", async (t) => {
  const T = 'bbbbbbbb-0000-4000-8000-0000000000b1'
  const ago = (ms) => new Date(Date.now() - ms).toISOString()
  const claudeOn = (provider) => ({ provider, sessionId: T, metadata: { provider: 'claude' } })
  // Started from Persona on one provider, then moved by Paseo to another: a new agent on the same Claude session.
  const older = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'Fix the build', status: 'idle', createdAt: ago(3_600_000),
    updatedAt: ago(1_800_000), labels: { 'persona.session': 'paseo-b1' }, persistence: claudeOn('my-claude') })
  const newer = agentSnapshot({ provider: 'old-claude', cwd: '/tmp', title: 'Fix the build', status: 'idle', createdAt: ago(600_000),
    updatedAt: ago(600_000), persistence: claudeOn('old-claude') })
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  const { fake, node, rows } = await setup(t, { ...OLD_CLAUDE, agents: [...base, { agent: older, project: PROJECT }, { agent: newer, project: PROJECT }] })
  const ours = () => rows().filter((r) => r.session_id === 'paseo-b1' || r.session_id === T)
  await until('listed', () => ours().length > 0)
  assert.deepEqual(ours().map((r) => [r.session_id, r.launcher]), [['paseo-b1', 'old-claude']], "one row, under the id Persona knows it by: the newer agent's")
  await call(node, 'session.send', { session_id: 'paseo-b1', text: 'go on' })
  assert.equal(fake.requestsOf('send_agent_message_request').at(-1).agentId, newer.id, 'to the newer agent')

  // The older one at work (its turn was still going in Paseo's app): the message goes to it, no second writer starts.
  fake.update(older.id, { status: 'running' })
  await until('the one at work', () => ours()[0]?.launcher === 'my-claude')
  await call(node, 'session.send', { session_id: 'paseo-b1', text: 'and the tests' })
  assert.equal(fake.requestsOf('send_agent_message_request').at(-1).agentId, older.id)
  fake.update(older.id, { status: 'idle' })
  await until('the newer one again', () => ours()[0]?.launcher === 'old-claude')

  // Both archived: one row, not running, under the same id; a message takes the newer one back from the archive.
  fake.archive(older.id)
  fake.archive(newer.id)
  await until('archived', () => ours().length === 1 && ours()[0].status === 'offline')
  assert.deepEqual(ours().map((r) => [r.session_id, r.launcher]), [['paseo-b1', 'old-claude']])
  assert.equal(node.last('runtime.sessions').unlisted_transcripts?.includes(T), false, 'its transcript is no second row')
  await call(node, 'session.send', { session_id: 'paseo-b1', text: 'once more' })
  assert.equal(fake.requestsOf('send_agent_message_request').at(-1).agentId, newer.id)
})

test("an agent on the Claude session of an older one archived before the bridge started takes that one's id once Paseo's archive is read (#8131)", async (t) => {
  const T = 'bbbbbbbb-0000-4000-8000-0000000000c1'
  const ago = (ms) => new Date(Date.now() - ms).toISOString()
  const older = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'Fix the build', status: 'closed', createdAt: ago(7_200_000),
    archivedAt: ago(3_600_000), updatedAt: ago(3_600_000), labels: { 'persona.session': 'paseo-c1' },
    persistence: { provider: 'my-claude', sessionId: T, metadata: { provider: 'claude' } } })
  const newer = agentSnapshot({ provider: 'old-claude', cwd: '/tmp', title: 'Fix the build', status: 'idle', createdAt: ago(1_800_000),
    updatedAt: ago(1_800_000), persistence: { provider: 'old-claude', sessionId: T, metadata: { provider: 'claude' } } })
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  const { fake, node, rows } = await setup(t, { ...OLD_CLAUDE, agents: [...base, { agent: older, project: PROJECT }, { agent: newer, project: PROJECT }] })
  await until('listed under the older id', () => rows().some((r) => r.session_id === 'paseo-c1'))
  assert.equal(rows().some((r) => r.session_id === T), false, 'no row under the Claude session')
  assert.equal(rows().find((r) => r.session_id === 'paseo-c1').status, 'idle', "the newer agent's row, not the archived one's")
  await call(node, 'session.send', { session_id: 'paseo-c1', text: 'go on' })
  assert.equal(fake.requestsOf('send_agent_message_request').at(-1).agentId, newer.id)
})

test("Paseo's archive at connect: archived agents that keep their id are rows, the latest of the week beside the list's limit; the agents Paseo lists come first; a deleted one leaves", async (t) => {
  const day = 86_400_000
  const transcript = (n) => `ffffffff-0000-4000-8000-${String(n).padStart(12, '0')}`
  const kept = (n, days, o = {}) => {
    const at = new Date(Date.now() - days * day - n * 1000).toISOString()
    return { agent: agentSnapshot({ id: `kept-${n}`, provider: 'my-claude', cwd: '/tmp', title: `K${n}`, status: 'closed', archivedAt: at, updatedAt: at,
      labels: { 'persona.session': `paseo-kept-${n}` }, persistence: { provider: 'my-claude', sessionId: transcript(n), metadata: { provider: 'claude' } }, ...o }),
      project: PROJECT }
  }
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  // 82 archived this week, one 9 days ago; one whose session id an agent Paseo lists has, one whose transcript another has.
  const later = new Date(Date.now() + 60_000).toISOString() // the newest: within the list's limit
  const live = [
    { agent: agentSnapshot({ id: 'live-1', provider: 'my-claude', cwd: '/tmp', title: 'L1', labels: { 'persona.session': 'paseo-kept-90' }, updatedAt: later,
      persistence: { provider: 'my-claude', sessionId: transcript(190), metadata: { provider: 'claude' } } }), project: PROJECT },
    { agent: agentSnapshot({ id: 'live-2', provider: 'my-claude', cwd: '/tmp', title: 'L2', updatedAt: later,
      persistence: { provider: 'my-claude', sessionId: transcript(91), metadata: { provider: 'claude' } } }), project: PROJECT },
  ]
  const archivedOnes = [...Array.from({ length: 82 }, (_, i) => kept(i + 1, 1)), kept(100, 9), kept(90, 1), kept(91, 1)]
  const { fake, node, bridge, paseo, rows } = await setup(t, { listMax: 4, sweepMs: 40, agents: [...base, ...live, ...archivedOnes] })
  const keptOf = (list) => list.filter((r) => r.session_id.startsWith('paseo-kept-') && r.status === 'offline')
  const keptRows = () => keptOf(rows())
  await until('rows of the archived ones', () => keptRows().length === 80)
  assert.deepEqual(keptRows().map((r) => r.title).sort(), Array.from({ length: 80 }, (_, i) => `K${i + 1}`).sort(),
    'the latest 80 of the week: not older ones, and not one whose id or transcript an agent Paseo lists has')
  assert.equal(rows().length - keptRows().length, 4, 'the list keeps its own limit')
  assert.ok(rows().some((r) => r.title === 'L1' && r.session_id === 'paseo-kept-90' && r.status !== 'offline'), 'the agent Paseo lists is the session')
  assert.equal(node.last('runtime.sessions').unlisted_transcripts.some((x) => x.startsWith('ffffffff-')), false, 'no transcript of theirs is named')
  // Deleted in Paseo while archived: found out by the next reading of the archive, its row leaves; the next one in.
  fake.delete('kept-1')
  await until('the deleted one gone', () => !rows().some((r) => r.session_id === 'paseo-kept-1'))
  await until('the next one listed', () => keptRows().some((r) => r.title === 'K81'))
  // The bridge again, from nothing (a restart): the same rows under the same ids, once it has read the archive.
  const ids = keptRows().map((r) => r.session_id).sort()
  bridge.stop()
  const again = createPaseoBridge({ paseo: new PaseoClient({ home: paseo.home, retryMin: 30, retryMax: 100 }) },
    { socketPath: node.path, retryMs: 30, pushDelayMs: 5, refreshMs: 0, callWait: 1500, listMax: 4, sweepMs: 0 })
  const mark = node.events.length
  again.start()
  t.after(() => again.stop())
  const fresh = () => node.events.slice(mark).filter((e) => e.event === 'runtime.sessions').at(-1)?.params.sessions ?? []
  await until('listed again', () => keptOf(fresh()).length === 80, 5000)
  assert.deepEqual(keptOf(fresh()).map((r) => r.session_id).sort(), ids)
})

test('an agent running Codex names its thread, an agent of Claude Code its transcript', async (t) => {
  const THREAD = '0199aaaa-bbbb-7ccc-8ddd-eeeeffff0002'
  const { fake, rows } = await setup(t)
  fake.upsert(agentSnapshot({ provider: 'my-codex', cwd: '/tmp', title: 'CX',
    persistence: { provider: 'my-codex', sessionId: THREAD, metadata: { provider: 'codex', threadId: THREAD } } }), PROJECT)
  await until('the agent', () => rows().some((r) => r.title === 'CX'))
  const by = Object.fromEntries(rows().map((r) => [r.title, r]))
  assert.deepEqual([by.CX.codex_thread, by.CX.transcript, by.A.codex_thread, by.A.transcript], [THREAD, undefined, undefined, SID],
    'the node lists a named thread once, as this session (#4495)')
})

test('a repository opened through a symbolic link is named by its physical directory', async (t) => {
  const dir = mkdtempSync('/tmp/ppaseo-ln-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  mkdirSync(join(dir, 'data', 'app'), { recursive: true })
  symlinkSync(join(dir, 'data'), join(dir, 'home'))
  const opened = join(dir, 'home', 'app') // as the project was opened in Paseo
  const physical = realpathSync(join(dir, 'data', 'app')) // the temporary directory may be behind a link itself
  const placed = { agent: agentSnapshot({ provider: 'my-codex', cwd: opened, workspaceId: 'wks_ln', title: 'LN' }),
    project: { projectKey: 'prj_ln', projectName: 'app', workspaceName: null,
      checkout: { cwd: opened, isGit: true, currentBranch: 'main', remoteUrl: null, worktreeRoot: opened, isPaseoOwnedWorktree: false, mainRepoRoot: null } } }
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  const { rows } = await setup(t, { agents: [...base, placed] })
  await until('the agent', () => rows().some((r) => r.title === 'LN'))
  const row = rows().find((r) => r.title === 'LN')
  assert.deepEqual([row.cwd, row.repo_root], [opened, physical], "its cwd as Paseo has it; the repository as the node names its working tree (#4492)")
})

test("delegated agents are their creator's subtasks: its session their parent, the unfinished ones its background work", async (t) => {
  const { fake, a, rows } = await setup(t)
  const ran = '2026-11-04T00:00:00Z'
  const kid = (o) => agentSnapshot({ provider: 'my-codex', cwd: '/tmp', labels: { 'paseo.parent-agent-id': a.claude.id, 'paseo.parent-server-id': 'srv_test' }, ...o })
  const k1 = kid({ title: 'K1', status: 'running' })
  const k2 = kid({ title: 'K2', status: 'idle', lastUserMessageAt: ran, requiresAttention: true, attentionReason: 'finished' })
  const far = kid({ title: 'FAR', status: 'idle', lastUserMessageAt: ran, requiresAttention: true, workspaceId: 'wks_far' })
  const lost = agentSnapshot({ provider: 'my-codex', cwd: '/tmp', title: 'LOST', status: 'idle', lastUserMessageAt: ran, requiresAttention: true,
    labels: { 'paseo.parent-agent-id': 'deleted-agent' } })
  for (const x of [k1, k2, far, lost]) fake.upsert(x, PROJECT)
  const titled = () => Object.fromEntries(rows().map((r) => [r.title, r]))
  await until('the delegated agents', () => ['K1', 'K2', 'FAR', 'LOST'].every((x) => titled()[x]))
  const by = titled()
  assert.deepEqual([by.K1.is_subagent, by.K1.parent_session_id, by.K1.status], [true, SID, 'working'])
  assert.deepEqual([by.K2.is_subagent, by.K2.parent_session_id, by.K2.status], [true, SID, 'idle'], 'finished: its creator was told, nobody else has to look')
  assert.deepEqual([by.FAR.is_subagent, by.FAR.parent_session_id, by.FAR.status], [undefined, undefined, 'done'],
    "created from another workspace it is that workspace's own agent, looked at like any other, as in Paseo's sidebar")
  assert.deepEqual([by.LOST.is_subagent, by.LOST.parent_session_id, by.LOST.status], [undefined, undefined, 'idle'],
    'its creator is gone: a row of its own, and as in Paseo nothing says it waits to be looked at')
  await until("the creator's background work", () => titled().A.background?.length === 1)
  assert.deepEqual(titled().A.background.map((b) => [b.id, b.kind, b.description]), [[`paseo-${k1.id}`, 'agent', 'K1']])
  assert.equal(titled().A.status, 'done', 'its own turn ended; the hub shows it working while its agents run')
  // A request of the delegated agent keeps it among the unfinished. Its row still names its creator: it is the node
  // that lists a session waiting for the user without its parent. Once it finished, nothing is left running.
  fake.update(k1.id, { status: 'idle', pendingPermissions: [{ id: 'perm-kid', provider: 'codex', name: 'exec', kind: 'tool', detail: { type: 'shell', command: 'make' } }] })
  await until('waiting for an answer', () => titled().K1.status === 'blocked')
  assert.deepEqual([titled().A.background?.length, titled().K1.parent_session_id], [1, SID])
  fake.update(k1.id, { status: 'idle', pendingPermissions: [] })
  await until('the fleet reported back', () => titled().K1.status === 'idle' && !titled().A.background)
  // The creator closed (Paseo let its process go) while one of its agents works: not Offline, their reports wake it.
  fake.update(k1.id, { status: 'running' })
  fake.update(a.claude.id, { status: 'closed' })
  await until('the creator waits for its agent', () => titled().A.status === 'idle' && titled().A.background?.length === 1)
  fake.update(k1.id, { status: 'idle' })
  await until('nothing of its own left running: offline', () => titled().A.status === 'offline' && !titled().A.background)
  assert.equal(titled().K2.parent_session_id, SID, 'still their parent')
})

test('an agent a delegated agent created is listed under it, however deep, and its card reaches the node; a ring of creators is rows of their own', async (t) => {
  const { fake, node, a, rows } = await setup(t, { strict: true })
  const kid = (id, by, o) => agentSnapshot({ id, provider: 'my-codex', cwd: '/tmp', title: id, labels: { 'paseo.parent-agent-id': by }, ...o })
  const list = [kid('MID', a.claude.id, { status: 'idle' }), kid('LEAF', 'MID', { status: 'running' }),
    kid('RING1', 'RING2', { status: 'idle', requiresAttention: true }), kid('RING2', 'RING1', { status: 'running' })]
  for (const x of list) fake.upsert(x, PROJECT)
  const titled = () => Object.fromEntries(rows().map((r) => [r.title, r]))
  await until('all of them', () => ['MID', 'LEAF', 'RING1', 'RING2'].every((x) => titled()[x]))
  const by = titled()
  assert.deepEqual([by.MID.parent_session_id, by.LEAF.parent_session_id], [SID, by.MID.session_id], 'each under the agent that created it')
  assert.deepEqual([by.RING1.parent_session_id, by.RING2.parent_session_id, by.RING1.status, by.RING2.status], [undefined, undefined, 'idle', 'working'],
    'agents that created one another: neither is under the other, and as in Paseo neither waits to be looked at')
  // MID's own turn ended, but the agent it created still runs: MID has not finished, and so neither has A.
  await until('unfinished up the line', () => titled().A.background?.length === 1)
  assert.deepEqual([titled().MID.background.map((b) => b.description), titled().A.background.map((b) => b.description)], [['LEAF'], ['MID']])
  // The node takes a card only for a session it was told of: LEAF's goes through, and it is answered.
  node.resolve = () => ({ allow: true })
  fake.permission('LEAF', { id: 'perm-leaf', provider: 'claude', name: 'Bash', kind: 'tool', input: { command: 'make' } })
  await until('answered', () => fake.requestsOf('agent_permission_response').length === 1)
  assert.deepEqual([node.refused.length, node.asked[0].session_id], [0, by.LEAF.session_id])
})

test("the list keeps the user's own agents first: delegated agents fill what is left, the busy ones and their creator always", async (t) => {
  const at = (n) => new Date(Date.UTC(2026, 10, 1, 0, n)).toISOString()
  const own = (n, o = {}) => agentSnapshot({ id: `own-${n}`, provider: 'my-codex', cwd: '/tmp', title: `OWN${n}`, status: 'idle', updatedAt: at(n), ...o })
  const kid = (n, parent, o = {}) => agentSnapshot({ id: `kid-${n}`, provider: 'my-codex', cwd: '/tmp', title: `KID${n}`, status: 'closed', updatedAt: at(n),
    labels: { 'paseo.parent-agent-id': parent }, ...o })
  // Newest last: the delegated agents are all newer than the user's own.
  const list = [own(1), own(2), own(3), kid(4, 'own-1', { status: 'running' }), kid(5, 'own-3'), kid(6, 'own-3'), kid(7, 'own-3'), kid(8, 'own-3')]
  const fake = await startFakePaseo(t, { providers: PROVIDERS, entries: ENTRIES, agents: list.map((agent) => ({ agent, project: PROJECT })) })
  const node = await startFakeNode(t, { features: FEATURES })
  const home = mkdtempSync('/tmp/ppaseo-')
  t.after(() => rmSync(home, { recursive: true, force: true }))
  writeFileSync(join(home, 'paseo.pid'), JSON.stringify({ pid: process.pid, listen: `127.0.0.1:${fake.port}` }))
  const paseo = new PaseoClient({ home, retryMin: 30, retryMax: 100 })
  const bridge = createPaseoBridge({ paseo }, { socketPath: node.path, retryMs: 30, pushDelayMs: 5, refreshMs: 0, awayGrace: 400, callWait: 1500, listMax: 5 })
  bridge.start()
  t.after(() => bridge.stop())
  await node.ready
  await until('the list', () => node.last('runtime.sessions')?.sessions?.length >= 5)
  assert.deepEqual(node.last('runtime.sessions').sessions.map((r) => r.title), ['KID8', 'KID4', 'OWN3', 'OWN2', 'OWN1'],
    "all of the user's own, though the delegated agents are newer; of those the working one, then the newest in what is left")
})

test('a busy agent keeps the whole line of its creators listed, past the list limit', async (t) => {
  const at = (n) => new Date(Date.UTC(2026, 10, 1, 0, n)).toISOString()
  const one = (id, n, o = {}) => agentSnapshot({ id, provider: 'my-codex', cwd: '/tmp', title: id, status: 'idle', updatedAt: at(n), ...o })
  const by = (id) => ({ labels: { 'paseo.parent-agent-id': id } })
  // The oldest of the user's own started a line: TOP created MID (long done), MID created LEAF (working).
  const list = [one('TOP', 1), one('OWN2', 2), one('OWN3', 3), one('OWN4', 4), one('MID', 5, { status: 'closed', ...by('TOP') }),
    one('LEAF', 6, { status: 'running', ...by('MID') }), one('IDLE', 7, { status: 'closed', ...by('OWN4') })]
  const fake = await startFakePaseo(t, { providers: PROVIDERS, entries: ENTRIES, agents: list.map((agent) => ({ agent, project: PROJECT })) })
  const node = await startFakeNode(t, { features: FEATURES })
  const home = mkdtempSync('/tmp/ppaseo-')
  t.after(() => rmSync(home, { recursive: true, force: true }))
  writeFileSync(join(home, 'paseo.pid'), JSON.stringify({ pid: process.pid, listen: `127.0.0.1:${fake.port}` }))
  const paseo = new PaseoClient({ home, retryMin: 30, retryMax: 100 })
  const bridge = createPaseoBridge({ paseo }, { socketPath: node.path, retryMs: 30, pushDelayMs: 5, refreshMs: 0, awayGrace: 400, callWait: 1500, listMax: 3 })
  bridge.start()
  t.after(() => bridge.stop())
  await node.ready
  await until('the list', () => node.last('runtime.sessions')?.sessions?.length >= 5)
  const rows = node.last('runtime.sessions').sessions
  assert.deepEqual(rows.map((r) => r.title), ['LEAF', 'MID', 'OWN4', 'OWN3', 'TOP'],
    'the working one, the creator between and the one the line starts at; the rest of the limit to the newest of the user\'s own')
  const sid = Object.fromEntries(rows.map((r) => [r.title, r.session_id]))
  assert.deepEqual(rows.filter((r) => r.parent_session_id).map((r) => [r.title, r.parent_session_id]), [['LEAF', sid.MID], ['MID', sid.TOP]],
    'no row names a parent that is not listed')
})

test("cards: Paseo's requests go to the phone and its answers back; one answered in Paseo leaves the phone", async (t) => {
  const { fake, node, a } = await setup(t)
  let answer
  node.resolve = (ix) => answer(ix)
  answer = () => ({ allow: true })
  fake.permission(a.claude.id, { id: 'permission-1', provider: 'claude', name: 'Bash', kind: 'tool', input: { command: 'npm test' }, metadata: { toolUseId: 'toolu_1' } })
  await until('the response', () => fake.requestsOf('agent_permission_response').length === 1)
  assert.deepEqual(node.asked[0].tool_input, { command: 'npm test' })
  assert.deepEqual([node.asked[0].kind, node.asked[0].session_id, node.asked[0].tool_name, node.asked[0].tool_use_id], ['permission', SID, 'Bash', 'toolu_1'])
  const r = fake.requestsOf('agent_permission_response')[0]
  assert.deepEqual([r.agentId, r.requestId, r.response], [a.claude.id, 'permission-1', { behavior: 'allow' }])

  answer = (ix) => ({ answers: { [ix.questions[0].question]: 'B' } })
  fake.permission(a.claude.id, { id: 'permission-2', provider: 'claude', name: 'AskUserQuestion', kind: 'question',
    input: { questions: [{ question: '选哪个？', header: '选择', multiSelect: false, options: [{ label: 'A' }, { label: 'B' }] }] } })
  await until('the answer', () => fake.requestsOf('agent_permission_response').length === 2)
  assert.deepEqual(fake.requestsOf('agent_permission_response')[1].response, { behavior: 'allow', updatedInput: { answers: { '选哪个？': 'B' } } })

  // Nobody answers on the phone; Paseo's own app does: the card is taken off the phone.
  answer = () => new Promise(() => {})
  fake.permission(a.claude.id, { id: 'permission-3', provider: 'claude', name: 'Edit', kind: 'tool', input: { file_path: '/a' } })
  await until('on the phone', () => node.asked.length === 3)
  fake.resolvePermission(a.claude.id, 'permission-3', { behavior: 'allow' })
  await until('withdrawn', () => node.all('interaction.cancel').some((c) => c.id === node.asked[2].id))
  assert.equal(fake.requestsOf('agent_permission_response').length, 2, 'nothing sent for it')

  // Handed back to the computer: left for Paseo's app.
  answer = () => ({ dismiss: true })
  fake.permission(a.claude.id, { id: 'permission-4', provider: 'claude', name: 'ExitPlanMode', kind: 'plan', input: { plan: 'p' }, metadata: { planText: '## 计划' },
    actions: [{ id: 'reject', behavior: 'deny' }, { id: 'implement', behavior: 'allow' }] })
  await until('asked', () => node.asked.length === 4)
  assert.equal(node.asked[3].plan, '## 计划')
  await new Promise((r) => setTimeout(r, 200))
  assert.equal(fake.requestsOf('agent_permission_response').length, 2)
})

test('an approved plan with a mode: implement, then that mode; the plan read from its call', async (t) => {
  const { fake, node, a } = await setup(t)
  node.resolve = () => ({ approve: true, mode: 'bypassPermissions' })
  // Claude Code gives the permission no input for ExitPlanMode: the plan is in the tool call on the timeline.
  fake.setTimeline(a.claude.id, [entry(7, { type: 'tool_call', callId: 'toolu_p', name: 'ExitPlanMode', status: 'running', error: null, detail: { type: 'plan', text: '## 计划\n\n1. 先写测试' } })])
  fake.permission(a.claude.id, { id: 'permission-9', provider: 'claude', name: 'ExitPlanMode', kind: 'plan', input: {}, metadata: { toolUseId: 'toolu_p' },
    actions: [{ id: 'reject', behavior: 'deny' }, { id: 'implement', behavior: 'allow' }] })
  await until('the mode set', () => fake.requestsOf('set_agent_mode_request').length === 1)
  assert.equal(node.asked[0].plan, '## 计划\n\n1. 先写测试')
  assert.deepEqual(fake.requestsOf('agent_permission_response')[0].response, { behavior: 'allow', selectedActionId: 'implement' })
  assert.equal(fake.requestsOf('set_agent_mode_request')[0].modeId, 'bypassPermissions')
})

// #8128: cards and their answers across link drops.

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const codexAsk = (id) => ({ id, provider: 'codex', name: 'exec', kind: 'tool', title: 'Run a command', detail: { type: 'shell', command: 'make' } })

test("an answer given while Paseo is away reaches it once it is back, and its card does not go up again", async (t) => {
  const { fake, node, bridge, a } = await setup(t, { awayGrace: 5000 })
  let answer
  node.resolve = () => new Promise((r) => { answer = r })
  fake.permission(a.codex.id, codexAsk('perm-away'))
  await until('on the phone', () => node.asked.length === 1 && answer)
  fake.refuse = 4500 // Paseo goes away (the laptop slept) and does not take the bridge back yet
  fake.drop()
  await until('the bridge sees Paseo gone', () => !bridge.paseo.connected)
  answer({ allow: true }) // the phone answers meanwhile
  await sleep(100)
  assert.equal(fake.requestsOf('agent_permission_response').length, 0)
  fake.refuse = undefined
  await until('the answer, once Paseo is back', () => fake.requestsOf('agent_permission_response').length === 1, 5000)
  const r = fake.requestsOf('agent_permission_response')[0]
  assert.deepEqual([r.agentId, r.requestId, r.response], [a.codex.id, 'perm-away', { behavior: 'allow' }])
  await until('settled', () => !bridge.state.asks.has('perm-away'))
  assert.equal(node.asked.length, 1, 'not asked a second time')
})

test("a card stays pending until Paseo confirms its answer; Paseo's resolution reaches the bridge's events too", async (t) => {
  const { fake, node, a } = await setup(t)
  let confirm
  fake.handlers.agent_permission_response = (msg, c) => {
    confirm = () => fake.resolvePermission(msg.agentId, msg.requestId, msg.response, c)
    return null // answered later
  }
  node.resolve = () => ({ allow: true })
  fake.permission(a.codex.id, codexAsk('perm-gap'))
  await until('the answer on its way', () => confirm)
  fake.update(a.codex.id, { title: 'B, still asking' }) // an update in between that still lists the request
  await sleep(150)
  assert.equal(node.asked.length, 1, 'not put on the phone again while its answer is on its way')
  confirm()
  await until('off the list', () => !node.last('runtime.sessions').sessions.find((s) => s.session_id === `paseo-${a.codex.id}`)?.status?.startsWith('blocked'))
  assert.equal(node.asked.length, 1)
})

test("a reply that is also a subscription's event reaches both: the answer settles, and the subscription hears the resolution", async (t) => {
  const a = agents()
  const fake = await startFakePaseo(t, { providers: PROVIDERS, entries: ENTRIES, agents: [{ agent: a.codex, project: PROJECT }] })
  const home = mkdtempSync('/tmp/ppaseo-')
  t.after(() => rmSync(home, { recursive: true, force: true }))
  writeFileSync(join(home, 'paseo.pid'), JSON.stringify({ pid: process.pid, listen: `127.0.0.1:${fake.port}` }))
  const paseo = new PaseoClient({ home, retryMin: 30, retryMax: 100 })
  const up = new Promise((r) => paseo.once('connected', r))
  paseo.start()
  t.after(() => paseo.stop())
  await up
  const heard = []
  await paseo.subscribe('session.events.set_subscription.request', { events: ['agent_permission_request', 'agent_permission_resolved'] }, (inner) => heard.push(inner))
  fake.permission(a.codex.id, codexAsk('perm-own'))
  // Paseo tells the subscriptions under the permission's id, the answer's requestId, before it replies to the answer.
  const reply = await paseo.request('agent_permission_response', { agentId: a.codex.id, response: { behavior: 'allow' } },
    { requestId: 'perm-own', expect: 'agent_permission_resolved' })
  assert.equal(reply.requestId, 'perm-own')
  await until('the subscription heard it', () => heard.some((m) => m.type === 'agent_permission_resolved' && m.payload.requestId === 'perm-own'))
})

test('a card on the phone when the hub link drops: asked again with the same id once the hub is back, however long it was up', async (t) => {
  const { fake, node, a } = await setup(t, { resumeWaitMs: 300 })
  let drop
  node.resolve = () => new Promise((_, reject) => { drop = () => reject(new RpcError('node going away', 'node_gone')) })
  fake.permission(a.codex.id, codexAsk('perm-drop'))
  await until('on the phone', () => drop)
  await sleep(400) // up longer than the wait for the next node
  // The node answers node_gone as its hub link drops, and tells the runtime the hub is gone after that.
  node.resolve = () => Promise.reject(new RpcError('no hub connected', 'node_gone'))
  drop()
  await sleep(20)
  node.setHub(false)
  await sleep(200)
  fake.permission(a.claude.id, { id: 'perm-meanwhile', provider: 'claude', name: 'Bash', kind: 'tool', input: { command: 'ls' } })
  await sleep(100)
  node.resolve = () => ({ allow: true })
  node.setHub(true)
  await until('both answered', () => fake.requestsOf('agent_permission_response').length === 2, 5000)
  const sent = Object.fromEntries(fake.requestsOf('agent_permission_response').map((r) => [r.requestId, r.response]))
  assert.deepEqual(sent, { 'perm-drop': { behavior: 'allow' }, 'perm-meanwhile': { behavior: 'allow' } })
  const drops = node.asked.filter((ix) => ix.session_id === `paseo-${a.codex.id}`)
  assert.ok(drops.length >= 2 && drops.every((ix) => ix.id === drops[0].id), 'the same card each time')
  assert.equal(node.asked.filter((ix) => ix.session_id === SID).length, 1, 'one asked with no hub went up once the hub was back')
})

test("a card the hub ended while it was away goes up again as a new card; a person's Return to computer stays", async (t) => {
  const { fake, node, a } = await setup(t, { resumeWaitMs: 200, atOnceMs: 300 })
  let drop
  node.resolve = () => new Promise((_, reject) => { drop = () => reject(new RpcError('node going away', 'node_gone')) })
  fake.permission(a.codex.id, codexAsk('perm-ended'))
  await until('on the phone', () => drop)
  const first = node.asked[0].id
  // The hub link drops for longer than the node waits for it (and than the hub keeps the card).
  node.resolve = () => Promise.reject(new RpcError('no hub connected', 'node_gone'))
  drop()
  node.setHub(false)
  await sleep(1500) // the runtime stops waiting: no hub, nothing handed back
  // Back: the hub answers the old card at once, from its record (cancelled); the new card is answered.
  node.resolve = (ix) => (ix.id === first ? { dismiss: true } : { allow: true })
  node.setHub(true)
  await until('the new card answered', () => fake.requestsOf('agent_permission_response').length === 1, 5000)
  const ids = node.asked.map((ix) => ix.id)
  assert.deepEqual(ids.slice(0, -1).every((id) => id === first), true)
  assert.notEqual(ids.at(-1), first, 'a new card: the hub keeps the old one as ended')
  assert.deepEqual(fake.requestsOf('agent_permission_response')[0].requestId, 'perm-ended')

  // A person hands one back (a while after it went up): it stays with Paseo's app, also across a hub flap.
  const asked = node.asked.length
  node.resolve = () => sleep(400).then(() => ({ dismiss: true }))
  fake.permission(a.codex.id, codexAsk('perm-person'))
  await until('handed back', () => node.asked.length === asked + 1)
  await sleep(600)
  node.setHub(false)
  node.setHub(true)
  await sleep(300)
  assert.equal(node.asked.length, asked + 1, 'not put up again')

  // A card the bridge has just made for a request was never on the hub: dismissed however soon, it is a person's.
  node.resolve = () => ({ dismiss: true })
  fake.permission(a.claude.id, { id: 'perm-quick', provider: 'claude', name: 'Bash', kind: 'tool', input: { command: 'ls' } })
  await until('asked', () => node.asked.length === asked + 3)
  await sleep(300)
  const quick = node.asked.slice(asked + 1)
  assert.equal(quick.length, 2, 'the id any bridge gives it (the hub may have ended it before), then one new card, and no more')
  assert.notEqual(quick[0].id, quick[1].id)
})

test("a card whose time ran out goes up again as a new card while Paseo still asks", async (t) => {
  const { fake, node, a } = await setup(t)
  let expire
  node.resolve = () => new Promise((r) => { expire = () => r({ dismiss: true }) })
  fake.permission(a.codex.id, codexAsk('perm-old'))
  await until('on the phone', () => expire)
  const realNow = Date.now
  t.after(() => { Date.now = realNow })
  node.resolve = () => new Promise(() => {})
  Date.now = () => realNow() + 30 * 60_000 // its 30 minutes are over: the hub ends it
  expire()
  await until('up again', () => node.asked.length === 2)
  Date.now = realNow
  assert.notEqual(node.asked[1].id, node.asked[0].id, 'a new card: the hub keeps the old one as expired')
  assert.deepEqual([node.asked[1].session_id, node.asked[1].tool_name, node.asked[1].summary], [node.asked[0].session_id, node.asked[0].tool_name, node.asked[0].summary])
  // Answered in Paseo's own app: the new card leaves the phone, and nothing goes up again.
  fake.resolvePermission(a.codex.id, 'perm-old', { behavior: 'allow' })
  await until('withdrawn', () => node.all('interaction.cancel').some((c) => c.id === node.asked[1].id))
  await sleep(100)
  assert.equal(node.asked.length, 2)
})

test('Paseo restarting: the bridge connects again and keeps the sessions; gone for good, it leaves the node', async (t) => {
  const { fake, node, rows, logs } = await setup(t, { awayGrace: 600 })
  const peer = node.peer
  fake.drop()
  await until('connected again', () => fake.hellos.length === 2)
  await until('subscribed again', () => [...fake.subs.values()].some((s) => s.kind === 'agents'))
  assert.equal(node.peer, peer, 'the node connection stayed')
  assert.ok(rows().length >= 4)
  fake.refuse = 4003
  fake.drop()
  await until('left the node', async () => {
    const gone = await Promise.race([peer.done.then(() => true), new Promise((r) => setTimeout(() => r(false), 50))])
    return gone
  }, 5000)
  assert.ok(logs.some((l) => /away/.test(l)))
})

// ---- what a reconnect leaves (#8129) ----

test("a reconnect's first page does not undo what Paseo said right after it: a turn that ended, a card answered, an agent made, one archived", async (t) => {
  const { fake, node, a, rows } = await setup(t)
  const row = (agent) => rows().find((r) => r.session_id === `paseo-${agent.id}`)
  node.resolve = () => new Promise(() => {}) // nobody answers on the phone
  fake.permission(a.claude.id, { id: 'permission-1', provider: 'claude', name: 'Bash', kind: 'tool', input: { command: 'npm test' } })
  await until('on the phone', () => node.asked.length === 1)
  const made = agentSnapshot({ provider: 'my-codex', cwd: '/tmp', title: 'MADE', status: 'running' })
  // While Paseo builds the next connection's first page: the Codex agent's turn ends (idle and marked for attention, its
  // updatedAt as it was: Paseo's onStreamTurnCompleted moves none), Claude Code answers its own card (nor does
  // onStreamPermissionResolved), an agent is made and Grok's is archived. Paseo sends those right after the page's reply
  // (held while it built it), here in the same read; the card's event goes out at once.
  fake.duringPage = (msg) => {
    if (msg.type !== 'fetch_agents_request' || !msg.subscribe) return
    fake.duringPage = undefined
    fake.upsert({ ...fake.agents.get(a.codex.id).agent, status: 'idle', requiresAttention: true })
    fake.upsert({ ...fake.agents.get(a.claude.id).agent, pendingPermissions: [] })
    fake.emit('events', { type: 'agent_permission_resolved', payload: { agentId: a.claude.id, requestId: 'permission-1', resolution: { behavior: 'allow' } } },
      (s) => s.events.includes('agent_permission_resolved'))
    fake.upsert(made, PROJECT)
    fake.archive(a.grok.id)
  }
  fake.drop()
  await until('connected again', () => fake.hellos.length === 2)
  await until('the archive read again', () => fake.requestsOf('fetch_agents_request').filter((r) => r.filter?.includeArchived).length === 2)
  await until('the agent made listed', () => row(made))
  await new Promise((r) => setTimeout(r, 100))
  assert.equal(row(a.codex)?.status, 'done', 'its turn ended: not Working again from the older page')
  assert.ok(node.all('interaction.cancel').some((c) => c.id === node.asked[0].id), 'the card answered meanwhile left the phone')
  assert.equal(node.asked.length, 1, 'and is not asked again from the older page')
  assert.notEqual(rows().find((r) => r.session_id === SID)?.status, 'blocked')
  assert.equal(row(made)?.status, 'working', 'made while the page was read: listed, though it is not on the page')
  assert.equal(row(a.grok), undefined, 'archived while the page was read: not listed again')
})

test("a reconnect's first page of workspaces does not undo what Paseo said right after it: one archived, one made", async (t) => {
  const dir = realpathSync(mkdtempSync('/tmp/ppaseo-ws-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const w = repoWorkspaces(dir)
  const { fake, node, bridge } = await setup(t, { workspaces: [w.main, w.wt] })
  await until('the workspaces', () => node.last('runtime.workspaces')?.workspaces?.length === 2)
  const notes = workspaceDescriptor({ id: 'wks_00000000000000c1', workspaceDirectory: dir, workspaceKind: 'directory', projectId: 'prj_notes',
    projectDisplayName: 'notes', name: 'notes', project: { projectKey: 'prj_notes', projectName: 'notes', workspaceName: null, checkout: { cwd: dir, isGit: false } } })
  fake.duringPage = (msg) => {
    if (msg.type !== 'fetch_workspaces_request' || !msg.subscribe) return
    fake.duringPage = undefined
    fake.removeWorkspace(w.wt.id)
    fake.upsertWorkspace(notes)
  }
  fake.drop()
  await until('connected again', () => fake.hellos.length === 2)
  const ids = () => node.last('runtime.workspaces').workspaces.map((x) => x.workspace_id).sort()
  await until('the list as Paseo has it', () => fake.requestsOf('fetch_workspaces_request').length === 2 && ids().join() === [w.main.id, notes.id].sort().join())
  await new Promise((r) => setTimeout(r, 300))
  assert.deepEqual(ids(), [w.main.id, notes.id].sort())
  assert.deepEqual([...bridge.state.workspaces.keys()].sort(), [w.main.id, notes.id].sort())
})

test('the directory read again takes what Paseo has now when an update was lost, never a page older than what Paseo said meanwhile', async (t) => {
  const { fake, a, rows, bridge } = await setup(t, { sweepMs: 60 })
  const status = () => rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.status
  await until('working', () => status() === 'working')
  // Its turn ended and the bridge was not told (no updatedAt moved): the next reading of the directory heals it.
  fake.agents.get(a.codex.id).agent = { ...fake.agents.get(a.codex.id).agent, status: 'idle', requiresAttention: true }
  await until('taken as Paseo has it', () => status() === 'done')
  // A turn starts while Paseo builds a page of the next reading: the list is told at once, ahead of that page's reply,
  // whose copy is from before (and the same updatedAt). The reading after it sees what that page left.
  let started = false
  let left
  fake.duringPage = (msg) => {
    if (msg.type !== 'fetch_agents_request' || msg.subscribe || msg.filter?.includeArchived) return
    if (!started) {
      started = true
      fake.upsert({ ...fake.agents.get(a.codex.id).agent, status: 'running', requiresAttention: false })
    } else left ??= bridge.state.agents.get(a.codex.id)?.agent.status
  }
  await until('read again after the turn started', () => left !== undefined)
  assert.equal(left, 'running', 'the page read before the turn started did not undo it')
  assert.equal(status(), 'working')
})

test('the directory read again takes nothing that is the same as kept: an unloaded agent still has the features last seen', async (t) => {
  const { fake, node, a, rows } = await setup(t, { sweepMs: 40 })
  // Grok's agent loaded with Allow all on, then let go (closed: Paseo's projection of its record lists no features).
  fake.update(a.grok.id, { status: 'idle', features: [autoAcceptFeature(true)] })
  await until('Allow all', () => rows().find((r) => r.session_id === `paseo-${a.grok.id}`)?.permission_mode === 'bypassPermissions')
  const { features: _, ...unloaded } = fake.agents.get(a.grok.id).agent
  fake.upsert({ ...unloaded, status: 'closed', updatedAt: new Date(Date.now() + 2).toISOString() })
  await until('closed', () => rows().find((r) => r.session_id === `paseo-${a.grok.id}`)?.status === 'offline')
  // And an agent the bridge does not list: a launcher check's probe (another bridge's, still in Paseo's directory).
  fake.upsert(agentSnapshot({ provider: 'my-claude', cwd: '/tmp', labels: { 'persona.check': '1' } }))
  await new Promise((r) => setTimeout(r, 150)) // the sweeps after it, the list told
  const pushes = node.all('runtime.sessions').length
  await new Promise((r) => setTimeout(r, 400)) // ten sweeps: nothing differs, nothing is told
  assert.equal(node.all('runtime.sessions').length, pushes, 'no sweep takes an agent again for what its page does not list')
  assert.equal(rows().find((r) => r.session_id === `paseo-${a.grok.id}`)?.permission_mode, 'bypassPermissions')
})

test("a failed read of Paseo's archive tells the node nothing, not that there are none; the sweep reads it again", async (t) => {
  const OLD = 'cccccccc-0000-4000-8000-000000000009'
  const at = new Date().toISOString()
  const archivedOne = agentSnapshot({ provider: 'my-claude', cwd: '/tmp', title: 'archived', status: 'closed', archivedAt: at, updatedAt: at,
    persistence: { provider: 'my-claude', sessionId: OLD, metadata: { provider: 'claude' } } })
  let fail = true
  const { node, logs } = await setup(t, {
    sweepMs: 80,
    agents: [...Object.values(agents()).map((agent) => ({ agent, project: PROJECT })), { agent: archivedOne, project: PROJECT }],
    handlers: { fetch_agents_request: (msg) => {
      if (msg.filter?.includeArchived && fail) return { type: 'rpc_error', payload: { requestType: msg.type, error: 'Paseo is busy', code: 'handler_error' } }
      // The sweep's directory, a page Paseo does not give: which agents are gone cannot be told, the archive is read all the same.
      if (!msg.subscribe && !msg.filter?.includeArchived) return { payload: { subscriptionId: null, entries: null, pageInfo: null } }
      return undefined
    } },
  })
  await until('the read at connect failed', () => logs.some((l) => /reading its archived agents: Paseo is busy/.test(l)))
  await until('and the sweep\'s', () => logs.some((l) => /reading its directory again: Paseo is busy/.test(l)))
  assert.ok(node.all('runtime.sessions').every((m) => m.unlisted_transcripts === undefined),
    'no list names the archived sessions as none: the node keeps what it was told before')
  fail = false
  await until('read by the sweep, and named', () => node.last('runtime.sessions').unlisted_transcripts?.includes(OLD))
})

test('the live text ends when the connection to Paseo drops; after the reconnect it starts afresh', async (t) => {
  const { fake, node, a } = await setup(t)
  const deltas = () => node.all('session.delta').filter((d) => d.session_id === SID)
  await call(node, 'session.watch', { session_id: SID, since_seq: 0, transcript: true })
  await until('followed', () => [...fake.subs.values()].some((s) => s.kind === 'timeline'))
  fake.stream(a.claude.id, { type: 'timeline', provider: 'claude', turnId: 't1', item: { type: 'assistant_message', text: 'Hello ', messageId: 'm1' } }, 5)
  await until('the live text', () => deltas().some((d) => d.text === 'Hello '))
  const before = deltas().length
  fake.drop()
  await until('ended at the drop', () => deltas().slice(before).some((d) => d.text === ''))
  await until('connected again', () => fake.hellos.length === 2)
  await until('followed again', () => [...fake.subs.values()].some((s) => s.kind === 'timeline'))
  // What was written while the connection was down never comes: the next chunk is a live text of its own.
  fake.stream(a.claude.id, { type: 'timeline', provider: 'claude', turnId: 't1', item: { type: 'assistant_message', text: 'again.', messageId: 'm1' } }, 7)
  await until('the next live text', () => deltas().at(-1)?.text.endsWith('again.'))
  assert.equal(deltas().at(-1).text, 'again.', 'not added to the text from before the drop')
})

test('an archived agent deleted in Paseo leaves at once: Paseo 0.15 tells every client of a deletion', async (t) => {
  const { fake, node, a, rows } = await setup(t)
  const pushed = () => node.last('runtime.sessions')
  await until('the archive read', () => pushed().unlisted_transcripts)
  // Archived: one that keeps its own id (a row not running), one named as its transcript.
  await call(node, 'session.close', { session_id: 'paseo-e1' })
  await until('a row not running', () => rows().find((r) => r.session_id === 'paseo-e1')?.status === 'offline')
  fake.archive(a.claude.id)
  await until('named', () => pushed().unlisted_transcripts?.includes(SID))
  const asked = fake.requestsOf('fetch_agent_request').length
  // Deleted in Paseo's own app: the list is told of the removal (no sweep in this test to find out otherwise).
  fake.delete(a.labeled.id, { told: true })
  fake.delete(a.claude.id, { told: true })
  await until('its row gone', () => !rows().some((r) => r.session_id === 'paseo-e1'))
  await until('named no more', () => !pushed().unlisted_transcripts?.includes(SID))
  assert.equal(fake.requestsOf('fetch_agent_request').length - asked, 2, 'asked about each once')
})

test("the keepalive: a hung Paseo is the bridge's own close, a dropped socket is said so, and a sleep is no silence", async (t) => {
  const { fake, logs } = await setup(t, { pingEvery: 50, silentFor: 200 })
  await new Promise((r) => setTimeout(r, 300))
  assert.equal(fake.hellos.length, 1, 'pings answered: one connection')
  // The process is held up (as across a sleep: its timers stop, the clock goes on), and nothing is read meanwhile.
  const until500 = Date.now() + 500
  while (Date.now() < until500) { /* held up */ }
  await new Promise((r) => setTimeout(r, 400))
  assert.equal(fake.hellos.length, 1, 'pinged first after the gap, and answered: the same connection')
  assert.equal(logs.some((l) => /no answer to pings/.test(l)), false)
  // Paseo stops answering, its socket open: the bridge closes it, and says so.
  fake.hang()
  await until('connected again', () => fake.hellos.length === 2)
  assert.ok(logs.some((l) => /no answer to pings/.test(l)))
  assert.ok(logs.includes('paseo: the bridge closed the connection (4000 keepalive timeout); trying again'), logs.join('\n'))
  // The socket ends with no close frame (Paseo killed): a dropped connection, not one Paseo closed.
  fake.drop()
  await until('connected once more', () => fake.hellos.length === 3)
  assert.ok(logs.includes('paseo: the connection to Paseo dropped (1006); trying again'), logs.join('\n'))
  assert.equal(logs.some((l) => /Paseo closed the connection/.test(l)), false, logs.join('\n'))
})

test("the hello names no app version: Paseo would read it as a version of its own app", async (t) => {
  const fake = await startFakePaseo(t, { providers: PROVIDERS, entries: ENTRIES })
  const node = await startFakeNode(t, { features: FEATURES })
  const home = mkdtempSync('/tmp/ppaseo-')
  t.after(() => rmSync(home, { recursive: true, force: true }))
  writeFileSync(join(home, 'paseo.pid'), JSON.stringify({ pid: process.pid, listen: `127.0.0.1:${fake.port}` }))
  // The bridge's own client, as bridge.mjs has it.
  const bridge = createPaseoBridge({}, { home, socketPath: node.path, retryMs: 30, pushDelayMs: 5, refreshMs: 0, sweepMs: 0 })
  bridge.start()
  t.after(() => bridge.stop())
  await until('the hello', () => fake.hellos.length === 1)
  assert.equal('appVersion' in fake.hellos[0], false)
  assert.deepEqual([fake.hellos[0].clientType, fake.hellos[0].capabilities.all_providers], ['cli', true])
})

test("an Agent Persona started calls its own Persona tools without a card; everything else still asks (#4966)", async (t) => {
  const own = ['mcp__persona__report_to_supervisor', 'mcp__persona__todo_decision_add']
  const { fake, node, a } = await setup(t, { allowTools: own })
  node.resolve = () => ({ allow: true })
  fake.permission(a.labeled.id, { id: 'own-1', provider: 'claude', name: 'mcp__persona__todo_decision_add', kind: 'tool',
    input: { decision: 'Use the smaller icon' }, suggestions: [{ type: 'addRules', rules: [{ toolName: 'mcp__persona__todo_decision_add' }], behavior: 'allow', destination: 'session' }] })
  await until('the own tool answered', () => fake.requestsOf('agent_permission_response').length === 1)
  assert.deepEqual(fake.requestsOf('agent_permission_response')[0].response, { behavior: 'allow' }, 'allowed once: no rule kept, no mode set')
  assert.equal(node.asked.length, 0, 'no card for it')
  // Still asked: dispatch and the phone's tools in the same Agent, any tool of an agent made in Paseo's own app, a plan.
  const asks = [
    [a.labeled.id, { id: 'dispatch-1', provider: 'claude', name: 'mcp__persona__dispatch_start', kind: 'tool', input: {} }],
    [a.labeled.id, { id: 'phone-1', provider: 'claude', name: 'mcp__persona__phone_notify', kind: 'tool', input: {} }],
    [a.claude.id, { id: 'mine-1', provider: 'claude', name: 'mcp__persona__report_to_supervisor', kind: 'tool', input: { kind: 'done', text: 'x' } }],
  ]
  for (const [agentId, request] of asks) fake.permission(agentId, request)
  await until('the others asked', () => node.asked.length === asks.length)
  assert.deepEqual(node.asked.map((ix) => [ix.session_id, ix.tool_name]).sort(), [
    ['paseo-e1', 'mcp__persona__dispatch_start'], ['paseo-e1', 'mcp__persona__phone_notify'], [SID, 'mcp__persona__report_to_supervisor'],
  ].sort())
  assert.equal(fake.requestsOf('set_agent_mode_request').length, 0)
})

test("an Agent Persona started on any provider calls its own Persona tools without a card: Grok, OpenCode, Codex (#8141)", async (t) => {
  const own = ['mcp__persona__report_to_supervisor', 'mcp__persona__todo_decision_add']
  const providers = { ...PROVIDERS, opencode: {} }
  const entries = [...ENTRIES, { provider: 'opencode', enabled: true, status: 'ready', label: 'OpenCode', models: [] }]
  const { fake, node, a, rows } = await setup(t, { allowTools: own, providers, entries })
  node.resolve = () => ({ allow: true })
  const started = (provider, sid) => {
    const x = agentSnapshot({ provider, cwd: '/tmp', title: sid, status: 'running', labels: { 'persona.session': sid } })
    fake.upsert(x, PROJECT)
    return x
  }
  const grok = started('grok', 'paseo-g1')
  const opencode = started('opencode', 'paseo-o1')
  const codex = started('my-codex', 'paseo-c1')
  await until('listed', () => ['paseo-g1', 'paseo-o1', 'paseo-c1'].every((id) => rows().some((r) => r.session_id === id)))
  // Grok over ACP names an MCP tool in its input; OpenCode by <server>_<tool>; Codex in its approval of the server.
  const useTool = (tool) => ({ type: 'unknown', input: { variant: 'UseTool', tool_name: tool, tool_input: { kind: 'done', text: 'x' } }, output: null })
  const elicitation = (server, tool) => ({ provider: 'codex', name: 'CodexMcpElicitation', kind: 'tool', title: `MCP approval: ${server}`,
    description: `Allow the ${server} MCP server to run tool "${tool}"?`, input: { mode: 'form', requestedSchema: null, url: null },
    metadata: { threadId: 'th-1', turnId: null, serverName: server, elicitationId: null } })
  const mine = [
    [grok.id, { id: 'g-own', provider: 'acp', name: 'other', kind: 'tool', title: 'Use tool', detail: useTool('persona__report_to_supervisor') }],
    [opencode.id, { id: 'o-own', provider: 'opencode', name: 'persona_todo_decision_add', kind: 'tool', title: 'Persona todo decision add', input: {} }],
    [codex.id, { id: 'c-own', ...elicitation('persona', 'report_to_supervisor') }],
  ]
  for (const [agentId, request] of mine) fake.permission(agentId, request)
  await until('the own tools answered', () => fake.requestsOf('agent_permission_response').length === mine.length)
  assert.deepEqual(fake.requestsOf('agent_permission_response').map((r) => [r.requestId, r.response]).sort(),
    [['c-own', { behavior: 'allow' }], ['g-own', { behavior: 'allow' }], ['o-own', { behavior: 'allow' }]], 'allowed once each: no rule kept, no mode set')
  // Still asked: another of Persona's tools in those Agents, another server's tool of the same name, and any tool of an
  // agent made in Paseo's own app.
  const asks = [
    [grok.id, { id: 'g-phone', provider: 'acp', name: 'other', kind: 'tool', title: 'Use tool', detail: useTool('persona__phone_notify') }],
    [opencode.id, { id: 'o-phone', provider: 'opencode', name: 'persona_phone_notify', kind: 'tool', title: 'Persona phone notify', input: {} }],
    [codex.id, { id: 'c-other', ...elicitation('other', 'report_to_supervisor') }],
    [a.grok.id, { id: 'g-mine', provider: 'acp', name: 'other', kind: 'tool', title: 'Use tool', detail: useTool('persona__report_to_supervisor') }],
  ]
  for (const [agentId, request] of asks) fake.permission(agentId, request)
  await until('the others asked', () => node.asked.length === asks.length)
  assert.deepEqual(node.asked.map((ix) => [ix.session_id, ix.tool_name]).sort(), [
    ['paseo-c1', 'CodexMcpElicitation'], ['paseo-g1', 'mcp__persona__phone_notify'], ['paseo-o1', 'persona_phone_notify'],
    [`paseo-${a.grok.id}`, 'mcp__persona__report_to_supervisor'],
  ].sort())
  assert.equal(fake.requestsOf('set_agent_mode_request').length + fake.requestsOf('set_agent_feature_request').length, 0)
})

for (const toolName of ['mcp__persona__report_to_supervisor', 'mcp__jarvis__report_to_supervisor']) {
  test(`a read-only ${toolName} runs once without permission or mode updates`, async (t) => {
    const { fake, node, a, rows } = await setup(t)
    fake.update(a.claude.id, { currentModeId: 'plan' })
    await until('plan mode', () => rows().find((r) => r.session_id === SID)?.permission_mode === 'plan')
    node.resolve = (ix) => {
      assert.equal(ix.kind, 'permission')
      assert.equal(ix.tool_name, toolName)
      return { allow: true }
    }
    const modesBefore = fake.requestsOf('set_agent_mode_request').length
    fake.permission(a.claude.id, { id: 'readonly-report', provider: 'claude', name: toolName, kind: 'tool',
      input: { kind: 'done', text: 'Inspection complete.' },
      suggestions: [{ type: 'addRules', rules: [{ toolName: toolName }], behavior: 'allow', destination: 'session' },
        { type: 'setMode', mode: 'bypassPermissions' }] })
    await until('report response', () => fake.requestsOf('agent_permission_response').length === 1)
    assert.deepEqual(fake.requestsOf('agent_permission_response')[0].response, { behavior: 'allow' })
    assert.equal(fake.requestsOf('set_agent_mode_request').length, modesBefore)
    assert.equal(rows().find((r) => r.session_id === SID)?.permission_mode, 'plan')
  })
}

test('forced close cancels a confirmation and retains the Agent on archive failure', async (t) => {
  const { fake, node, a, rows } = await setup(t)
  fake.update(a.claude.id, { pendingPermissions: [{ id: 'confirm-close', provider: 'claude', name: 'Bash', input: { command: 'echo test' } }] })
  await until('confirmation blocked', () => rows().find(r => r.session_id === SID)?.status === 'blocked')
  fake.handlers.archive_agent_request = () => ({ type: 'agent_archived', payload: { agentId: a.claude.id, error: 'Archive refused' } })
  await assert.rejects(call(node, 'session.close', { session_id: SID, force: true }), /Archive refused/)
  assert.ok(rows().some(r => r.session_id === SID), 'failed close remains visible')
  delete fake.handlers.archive_agent_request
  await call(node, 'session.close', { session_id: SID, force: true })
  assert.equal(fake.requestsOf('archive_agent_request').at(-1).agentId, a.claude.id)
  assert.equal(fake.requestsOf('agent_permission_response').filter(r => r.response?.behavior === 'allow').length, 0)
  await until('force-archived Agent gone', () => !rows().some(r => r.session_id === SID))
})

// ---- Paseo's workspaces (#6295) ----

/** Paseo's checkout of a repository, a worktree of it, and a background workspace, as its directory lists them. */
function repoWorkspaces(dir) {
  const app = join(dir, 'app')
  const tree = join(dir, 'trees', 'abcd1234', 'feat-x')
  mkdirSync(app, { recursive: true })
  mkdirSync(tree, { recursive: true })
  const checkout = (cwd, o = {}) => ({ cwd, isGit: true, currentBranch: o.branch ?? 'main', worktreeRoot: cwd, isPaseoOwnedWorktree: o.paseo ?? false, mainRepoRoot: o.main ?? null })
  const main = workspaceDescriptor({ id: 'wks_00000000000000a1', workspaceDirectory: app, projectId: 'prj_app', projectDisplayName: 'app', name: 'main',
    project: { projectKey: 'prj_app', projectName: 'app', workspaceName: null, checkout: checkout(app) } })
  const wt = workspaceDescriptor({ id: 'wks_00000000000000a2', workspaceDirectory: tree, projectId: 'prj_app', projectDisplayName: 'app', name: 'Login fix',
    workspaceKind: 'worktree', worktreeSlug: 'feat-x', gitRuntime: { currentBranch: 'feat/x', isPaseoOwnedWorktree: true, isDirty: false },
    project: { projectKey: 'prj_app', projectName: 'app', workspaceName: 'Login fix', checkout: checkout(tree, { branch: 'feat/x', paseo: true, main: app }) } })
  const hidden = workspaceDescriptor({ id: 'wks_00000000000000a3', workspaceDirectory: app, background: true })
  return { app, tree, main, wt, hidden }
}

test("Paseo's workspaces are the node's, agents in them or not: their kind, branch and space; a change that lists nothing new says nothing", async (t) => {
  const dir = realpathSync(mkdtempSync('/tmp/ppaseo-ws-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const w = repoWorkspaces(dir)
  const { fake, node, a, rows } = await setup(t, { workspaces: [w.main, w.wt, w.hidden] })
  const listed = await until('the workspaces', () => node.last('runtime.workspaces')?.workspaces)
  assert.deepEqual(listed, [
    { workspace_id: w.main.id, title: 'main', cwd: w.app, kind: 'checkout', branch: 'main', space_id: 'prj_app', space_name: 'app', repo_root: w.app },
    { workspace_id: w.wt.id, title: 'Login fix', cwd: w.tree, kind: 'worktree', branch: 'feat/x', space_id: 'prj_app', space_name: 'app', repo_root: w.app, owned: true },
  ], 'not the background one; no agents in either')
  assert.ok(fake.requestsOf('fetch_workspaces_request')[0].subscribe, 'followed')
  const said = node.all('runtime.workspaces').length
  fake.upsertWorkspace({ ...w.main, status: 'running', activityAt: new Date().toISOString() })
  const notes = workspaceDescriptor({ id: 'wks_00000000000000b1', workspaceDirectory: dir, workspaceKind: 'directory', projectId: 'prj_notes', projectDisplayName: 'notes', name: 'notes',
    project: { projectKey: 'prj_notes', projectName: 'notes', workspaceName: null, checkout: { cwd: dir, isGit: false } } })
  fake.upsertWorkspace(notes)
  await until('the new one', () => node.last('runtime.workspaces').workspaces.some((x) => x.workspace_id === notes.id))
  assert.equal(node.all('runtime.workspaces').length, said + 1, 'a status alone is no change of the list')
  assert.deepEqual(node.last('runtime.workspaces').workspaces.find((x) => x.workspace_id === notes.id),
    { workspace_id: notes.id, title: 'notes', cwd: dir, kind: 'folder', space_id: 'prj_notes', space_name: 'notes' })
  fake.removeWorkspace(w.wt.id)
  await until('archived: gone', () => !node.last('runtime.workspaces').workspaces.some((x) => x.workspace_id === w.wt.id))
  // An agent's row names its workspace.
  assert.equal(rows().find((r) => r.session_id === SID)?.workspace_id, a.claude.workspaceId)
})

test('a session starts in a workspace that has no agents; a workspace Paseo no longer has is refused; a project or folder still names none', async (t) => {
  const dir = realpathSync(mkdtempSync('/tmp/ppaseo-ws-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const w = repoWorkspaces(dir)
  const { fake, node } = await setup(t, { workspaces: [w.main, w.wt] })
  await until('the workspaces', () => node.last('runtime.workspaces'))
  const started = (params) => call(node, 'session.start', { prompt: 'x', launcher: 'my-codex', ...params }).then(() => fake.requestsOf('agent.create.request').at(-1))
  assert.equal((await started({ cwd: w.app, workspace_id: w.main.id })).workspaceId, w.main.id, 'named, with no agents in it')
  assert.equal((await started({ cwd: w.tree })).workspaceId, w.wt.id, 'a worktree by its folder, with no agents in it')
  assert.equal((await started({ cwd: w.app, workspace_id: 'prj_app' })).workspaceId, undefined, 'a space: a new workspace of the project, as before')
  const before = fake.requestsOf('agent.create.request').length
  await assert.rejects(started({ cwd: w.app, workspace_id: 'wks_00000000000000ff' }), (err) => err.code === 'workspace_missing')
  assert.equal(fake.requestsOf('agent.create.request').length, before, 'nothing created elsewhere')
  // Still being created: the row it will have is in the workspace's project, named after it.
  fake.handlers['agent.create.request'] = () => null
  const slow = await call(node, 'session.start', { prompt: 'x', launcher: 'my-codex', cwd: w.tree, workspace_id: w.wt.id })
  assert.deepEqual([slow.session.space_id, slow.session.slot_name, slow.session.workspace_id], ['prj_app', 'Login fix', w.wt.id])
})

test("worktrees made by Paseo: from the workspace's folder, a new branch cut from its base or one checked out, no agent with it; where they go", async (t) => {
  const dir = realpathSync(mkdtempSync('/tmp/ppaseo-ws-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const w = repoWorkspaces(dir)
  const { fake, node, paseo } = await setup(t, { workspaces: [w.main, w.wt], worktreesRoot: join(dir, 'trees', 'abcd1234') })
  await until('the workspaces', () => node.last('runtime.workspaces'))
  assert.deepEqual(await call(node, 'workspace.prepare', { cwd: w.app, repo_root: w.app }), { checkouts: join(dir, 'trees', 'abcd1234') },
    'beside a worktree Paseo made of the repository')
  const other = join(dir, 'other')
  mkdirSync(other)
  const { checkouts } = await call(node, 'workspace.prepare', { cwd: other, repo_root: other })
  assert.match(checkouts, new RegExp(`^${paseo.home}/worktrees/[0-9a-z]{8}$`), "else where Paseo's own code puts them")

  const made = await call(node, 'workspace.create', { cwd: w.app, workspace_id: w.main.id, branch: 'feat/y', create_branch: true, base: 'main', name: 'feat-y', key: 'persona-k1' })
  const req = fake.requestsOf('workspace.create.request').at(-1)
  assert.deepEqual(req.source, { kind: 'worktree', cwd: w.app, action: 'branch-off', refName: 'main', branchName: 'feat/y', worktreeSlug: 'feat-y', projectId: 'prj_app' },
    "the named workspace's own project")
  assert.equal(req.idempotencyKey, 'persona-k1')
  assert.equal(req.agent, undefined, 'no agent: Paseo would rename the branch after its prompt')
  assert.equal(req.firstAgentContext, undefined)
  assert.deepEqual(made.workspace, { workspace_id: made.workspace.workspace_id, title: 'feat/y', cwd: join(dir, 'trees', 'abcd1234', 'feat-y'), kind: 'worktree',
    branch: 'feat/y', space_id: 'prj_1', space_name: 'proj', repo_root: w.app, owned: true })
  await until('listed', () => node.last('runtime.workspaces').workspaces.some((x) => x.workspace_id === made.workspace.workspace_id))
  // A session at its folder goes into it, though Paseo's answer had no placement.
  mkdirSync(made.workspace.cwd, { recursive: true })
  await call(node, 'session.start', { prompt: 'x', launcher: 'my-codex', cwd: made.workspace.cwd })
  assert.equal(fake.requestsOf('agent.create.request').at(-1).workspaceId, made.workspace.workspace_id)

  await call(node, 'workspace.create', { cwd: w.app, branch: 'spare', create_branch: false, name: 'spare' })
  assert.deepEqual(fake.requestsOf('workspace.create.request').at(-1).source, { kind: 'worktree', cwd: w.app, action: 'checkout', refName: 'spare', worktreeSlug: 'spare' })
  await call(node, 'workspace.create', { cwd: w.app, create_branch: true, base: 'main' })
  assert.deepEqual(fake.requestsOf('workspace.create.request').at(-1).source, { kind: 'worktree', cwd: w.app, action: 'branch-off', refName: 'main' },
    "no branch, no name: Paseo's own")
  await assert.rejects(call(node, 'workspace.create', { cwd: w.app, workspace_id: 'wks_00000000000000ff', branch: 'x', create_branch: true }), (err) => err.code === 'workspace_missing')
  fake.handlers['workspace.create.request'] = () => ({ payload: { workspace: null, setupTerminalId: null, error: 'Create worktree requires a git repository', errorCode: 'not_git' } })
  await assert.rejects(call(node, 'workspace.create', { cwd: w.app, branch: 'x', create_branch: true }), /requires a git repository/)
})

// Real folders and Git checkouts: project identity must survive aliases without merging independent clones.
function projectFixture(t) {
  const dir = realpathSync(mkdtempSync('/tmp/ppaseo-project-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const repo = join(dir, 'app')
  mkdirSync(repo)
  const git = (cwd, ...args) => execFileSync('git', ['-C', cwd, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim()
  git(repo, 'init', '-b', 'main')
  git(repo, '-c', 'user.name=Test', '-c', 'user.email=test@example.com', 'commit', '--allow-empty', '-m', 'Initial')
  const alias = join(dir, 'alias')
  symlinkSync(repo, alias)
  const project = (id, root) => ({ projectId: id, projectKind: 'git', projectRootPath: root })
  return { dir, repo, alias, git, project }
}

const makeTree = (node, cwd, extra = {}) => call(node, 'workspace.create', { cwd, branch: 'feature', create_branch: true, base: 'main', ...extra })

test('cwd-only legacy worktree duplicates prefer the main project; explicit selection is unchanged', async (t) => {
  const f = projectFixture(t)
  const tree = join(f.dir, 'legacy')
  f.git(f.repo, 'worktree', 'add', '-b', 'legacy', tree)
  const legacy = workspaceDescriptor({ workspaceDirectory: tree, projectRootPath: tree, projectId: 'prj_legacy', projectDisplayName: 'legacy', name: 'legacy', workspaceKind: 'worktree',
    project: { projectKey: 'prj_legacy', projectName: 'legacy', workspaceName: 'legacy',
      checkout: { cwd: tree, isGit: true, currentBranch: 'legacy', worktreeRoot: tree, mainRepoRoot: f.repo, isPaseoOwnedWorktree: false } } })
  const { fake, node } = await setup(t, { workspaces: [legacy] })
  await until('the legacy workspace', () => node.last('runtime.workspaces'))
  const projects = [f.project('prj_legacy', tree), f.project('prj_main', f.alias)]
  fake.handlers['project.list.request'] = () => ({ payload: { projects } })

  await call(node, 'session.start', { cwd: tree, launcher: 'my-codex' })
  const made = fake.requestsOf('workspace.create.request').at(-1)
  assert.ok(made, 'cwd-only start creates a workspace under the main project')
  assert.deepEqual(made.source, { kind: 'directory', path: tree, projectId: 'prj_main' })
  assert.equal(projects.length, 2, 'no project ID is allocated')
  const mainWorkspaceId = fake.requestsOf('agent.create.request').at(-1).workspaceId
  assert.notEqual(mainWorkspaceId, legacy.id)

  const workspaceCreates = fake.requestsOf('workspace.create.request').length
  await call(node, 'session.start', { cwd: tree, launcher: 'my-codex' })
  assert.equal(fake.requestsOf('agent.create.request').at(-1).workspaceId, mainWorkspaceId, 'the main-project workspace is reused')
  assert.equal(fake.requestsOf('workspace.create.request').length, workspaceCreates)
  await call(node, 'session.start', { cwd: tree, workspace_id: legacy.id, launcher: 'my-codex' })
  assert.equal(fake.requestsOf('agent.create.request').at(-1).workspaceId, legacy.id)
  assert.equal(fake.requestsOf('workspace.create.request').length, workspaceCreates, 'an explicit workspace needs no replacement')
  assert.ok(fake.workspaces.has(legacy.id), 'the legacy record remains readable and untouched')
})

test('a cwd-only start that finds its existing worktree workspace does not need the project list', async (t) => {
  const f = projectFixture(t)
  const tree = join(f.dir, 'legacy')
  f.git(f.repo, 'worktree', 'add', '-b', 'legacy', tree)
  const legacy = workspaceDescriptor({ workspaceDirectory: tree, projectRootPath: tree, projectId: 'prj_legacy', projectDisplayName: 'legacy', name: 'legacy', workspaceKind: 'worktree',
    project: { projectKey: 'prj_legacy', projectName: 'legacy', workspaceName: 'legacy',
      checkout: { cwd: tree, isGit: true, currentBranch: 'legacy', worktreeRoot: tree, mainRepoRoot: f.repo, isPaseoOwnedWorktree: false } } })
  const { fake, node } = await setup(t, { workspaces: [legacy] })
  await until('the legacy workspace', () => node.last('runtime.workspaces'))
  fake.handlers['project.list.request'] = () => ({ payload: { error: 'project list is down' } })
  await call(node, 'session.start', { cwd: tree, launcher: 'my-codex' })
  assert.equal(fake.requestsOf('agent.create.request').at(-1)?.workspaceId, legacy.id, 'reuses the existing worktree workspace')
})

test('a cwd-only start falls back to an existing workspace when project roots are ambiguous', async (t) => {
  const f = projectFixture(t)
  const tree = join(f.dir, 'legacy')
  f.git(f.repo, 'worktree', 'add', '-b', 'legacy', tree)
  const subdir = join(tree, 'sub')
  mkdirSync(subdir)
  const legacy = workspaceDescriptor({ workspaceDirectory: tree, projectRootPath: tree, projectId: 'prj_legacy', projectDisplayName: 'legacy', name: 'legacy', workspaceKind: 'worktree',
    project: { projectKey: 'prj_legacy', projectName: 'legacy', workspaceName: 'legacy',
      checkout: { cwd: tree, isGit: true, currentBranch: 'legacy', worktreeRoot: tree, mainRepoRoot: f.repo, isPaseoOwnedWorktree: false } } })
  const existing = agentSnapshot({ provider: 'my-codex', cwd: subdir, workspaceId: legacy.id, status: 'idle' })
  const base = Object.values(agents()).map((agent) => ({ agent, project: PROJECT }))
  const { fake, node } = await setup(t, { workspaces: [legacy], agents: [...base, { agent: existing, project: legacy.project }] })
  await until('the legacy workspace', () => node.last('runtime.workspaces'))
  fake.handlers['project.list.request'] = () => ({ payload: { projects: [f.project('prj_tree', tree), f.project('prj_subdir', subdir)] } })
  await call(node, 'session.start', { cwd: subdir, launcher: 'my-codex' })
  assert.equal(fake.requestsOf('agent.create.request').at(-1)?.workspaceId, legacy.id, 'reuses the existing workspace despite ambiguous roots')
  assert.equal(fake.requestsOf('workspace.create.request').length, 0, 'no replacement workspace is created')
})

test('path-only worktrees and direct Agent starts reuse the unique physical project, including a non-git directory', async (t) => {
  const f = projectFixture(t)
  const { fake, node } = await setup(t)
  let projects = [f.project('prj_original', f.alias)]
  fake.handlers['project.list.request'] = () => ({ payload: { projects } })
  await makeTree(node, f.repo)
  assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_original')
  await call(node, 'session.start', { cwd: f.repo, launcher: 'my-codex' })
  let req = fake.requestsOf('workspace.create.request').at(-1)
  assert.deepEqual(req.source, { kind: 'directory', path: f.repo, projectId: 'prj_original' })
  assert.match(req.idempotencyKey, /^persona:workspace:paseo-/)
  assert.ok(fake.workspaces.has(fake.requestsOf('agent.create.request').at(-1).workspaceId))
  const folder = join(f.dir, 'notes')
  const folderAlias = join(f.dir, 'notes-alias')
  mkdirSync(folder)
  symlinkSync(folder, folderAlias)
  projects = [{ ...f.project('prj_notes', folderAlias), projectKind: 'non_git' }]
  await call(node, 'session.start', { cwd: folder, launcher: 'my-codex' })
  req = fake.requestsOf('workspace.create.request').at(-1)
  assert.deepEqual(req.source, { kind: 'directory', path: folder, projectId: 'prj_notes' })
})

test('projects at different non-main roots remain ambiguous and create nothing', async (t) => {
  const f = projectFixture(t)
  const tree = join(f.dir, 'ambiguous')
  f.git(f.repo, 'worktree', 'add', '-b', 'ambiguous', tree)
  const subdir = join(tree, 'sub')
  mkdirSync(subdir)
  const { fake, node } = await setup(t)
  fake.handlers['project.list.request'] = () => ({ payload: { projects: [f.project('prj_tree', tree), f.project('prj_subdir', subdir)] } })

  await assert.rejects(makeTree(node, subdir), (e) => e.code === 'project_ambiguous')
  await assert.rejects(call(node, 'session.start', { cwd: subdir, launcher: 'my-codex' }), (e) => e.code === 'project_ambiguous')
  assert.equal(fake.requestsOf('workspace.create.request').length, 0)
  assert.equal(fake.requestsOf('agent.create.request').length, 0)
})

test('a distinct project at a selected main-checkout subdirectory stays ambiguous', async (t) => {
  const f = projectFixture(t)
  const subdir = join(f.repo, 'sub')
  mkdirSync(subdir)
  const { fake, node } = await setup(t)
  fake.handlers['project.list.request'] = () => ({ payload: { projects: [f.project('prj_main', f.repo), f.project('prj_subdir', subdir)] } })

  await assert.rejects(call(node, 'session.start', { cwd: subdir, launcher: 'my-codex' }), (e) => e.code === 'project_ambiguous')
  await assert.rejects(makeTree(node, subdir), (e) => e.code === 'project_ambiguous')
  assert.equal(fake.requestsOf('workspace.create.request').length, 0)
  assert.equal(fake.requestsOf('agent.create.request').length, 0)
})

test('duplicate projects at one physical root resolve deterministically; an explicit workspace keeps its own project', async (t) => {
  const f = projectFixture(t)
  const w = workspaceDescriptor({ workspaceDirectory: f.alias, projectId: 'prj_original' })
  const { fake, node } = await setup(t, { workspaces: [w] })
  await until('the workspace', () => node.last('runtime.workspaces'))
  let projects = [f.project('prj_duplicate', f.repo), f.project('prj_original', f.alias)]
  fake.handlers['project.list.request'] = () => ({ payload: { projects } })
  for (const order of [projects, [...projects].reverse()]) {
    projects = order
    await assert.doesNotReject(makeTree(node, f.repo), 'duplicate main-root records must not refuse creation')
    assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_duplicate')
    await assert.doesNotReject(call(node, 'session.start', { cwd: f.repo, launcher: 'my-codex' }), 'duplicate main-root records must not refuse an Agent start')
    const agent = fake.requestsOf('agent.create.request').at(-1)
    assert.equal(fake.workspaces.get(agent.workspaceId)?.projectId, 'prj_duplicate')
  }
  assert.ok(fake.requestsOf('workspace.create.request').every((r) => r.source.projectId === 'prj_duplicate'))
  assert.equal(fake.requestsOf('agent.create.request').length, 2)
  fake.handlers['project.list.request'] = () => ({ type: 'rpc_error', payload: { error: 'Unavailable', code: 'unavailable' } })
  await makeTree(node, f.repo, { workspace_id: w.id })
  assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_original')
  await call(node, 'session.start', { cwd: f.alias, workspace_id: w.id, launcher: 'my-codex' })
  assert.equal(fake.requestsOf('agent.create.request').at(-1).workspaceId, w.id)
})

test('symlink-alias duplicate projects at one physical root resolve deterministically to the canonical path', async (t) => {
  const f = projectFixture(t)
  const { fake, node } = await setup(t)
  let projects = [f.project('prj_canonical', f.repo), f.project('prj_alias', f.alias)]
  fake.handlers['project.list.request'] = () => ({ payload: { projects } })
  for (const order of [projects, [...projects].reverse()]) {
    projects = order
    await makeTree(node, f.alias)
    assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_canonical')
    await call(node, 'session.start', { cwd: f.alias, launcher: 'my-codex' })
    assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_canonical')
  }
})

test('symlink-alias duplicates with no record at the physical path prefer the project with more workspaces, then the lowest ID', async (t) => {
  const f = projectFixture(t)
  const alias2 = join(f.dir, 'alias2')
  symlinkSync(f.repo, alias2)
  const w = workspaceDescriptor({ workspaceDirectory: alias2, projectId: 'prj_zbusy' })
  const { fake, node } = await setup(t, { workspaces: [w] })
  await until('the workspace', () => node.last('runtime.workspaces'))
  let projects = [f.project('prj_aidle', f.alias), f.project('prj_zbusy', alias2)]
  fake.handlers['project.list.request'] = () => ({ payload: { projects } })
  for (const order of [projects, [...projects].reverse()]) {
    projects = order
    await makeTree(node, f.repo)
    assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_zbusy', 'more workspaces beats a lower ID')
  }
  projects = [f.project('prj_b', f.alias), f.project('prj_a', alias2)]
  for (const order of [projects, [...projects].reverse()]) {
    projects = order
    await makeTree(node, f.repo)
    assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_a', 'equal workspaces fall back to the lowest ID')
  }
})

test('a linked worktree start picks the canonical record among its main checkout\'s symlink duplicates', async (t) => {
  const f = projectFixture(t)
  const tree = join(f.dir, 'linked')
  f.git(f.repo, 'worktree', 'add', '-b', 'linked', tree)
  const { fake, node } = await setup(t)
  let projects = [f.project('prj_legacy', tree), f.project('prj_alias', f.alias), f.project('prj_main', f.repo)]
  fake.handlers['project.list.request'] = () => ({ payload: { projects } })
  for (const order of [projects, [...projects].reverse()]) {
    projects = order
    await call(node, 'session.start', { cwd: tree, launcher: 'my-codex' })
    assert.deepEqual(fake.requestsOf('workspace.create.request').at(-1).source, { kind: 'directory', path: tree, projectId: 'prj_main' })
  }
})

test('path-only creation with no existing Paseo project canonicalizes symlink cwd before calling Paseo', async (t) => {
  const f = projectFixture(t)
  const { fake, node } = await setup(t)
  fake.handlers['project.list.request'] = () => ({ payload: { projects: [] } })
  await makeTree(node, f.alias)
  assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.cwd, f.repo)
  await call(node, 'session.start', { cwd: f.alias, launcher: 'my-codex' })
  assert.equal(fake.requestsOf('agent.create.request').at(-1).config.cwd, f.repo)
})

for (const [name, reply] of [
  ['RPC failure', { type: 'rpc_error', payload: { error: 'List unavailable', code: 'unavailable' } }],
  ['missing list', { payload: {} }],
  ['error with an empty list', { payload: { projects: [], error: 'Storage failed' } }],
  ['invalid project', { payload: { projects: [{ projectRootPath: '/tmp' }] } }],
]) {
  test(`project discovery ${name} creates no workspace or Agent`, async (t) => {
    const f = projectFixture(t)
    const { fake, node } = await setup(t)
    fake.handlers['project.list.request'] = () => reply
    await assert.rejects(makeTree(node, f.repo), (e) => e.code === 'project_unavailable')
    await assert.rejects(call(node, 'session.start', { cwd: f.repo, launcher: 'my-codex' }), (e) => e.code === 'project_unavailable')
    assert.equal(fake.requestsOf('workspace.create.request').length, 0)
    assert.equal(fake.requestsOf('agent.create.request').length, 0)
  })
}

test('linked worktrees and subdirectories reuse the main project; a clone with the same remote stays separate', async (t) => {
  const f = projectFixture(t)
  const tree = join(f.dir, 'linked')
  f.git(f.repo, 'worktree', 'add', '-b', 'linked', tree)
  const subdir = join(tree, 'sub')
  mkdirSync(subdir)
  const clone = join(f.dir, 'clone')
  f.git(f.dir, 'clone', f.repo, clone)
  const remote = 'https://example.com/repo.git'
  f.git(f.repo, 'remote', 'add', 'origin', remote)
  f.git(clone, 'remote', 'set-url', 'origin', remote)
  const { fake, node } = await setup(t)
  let projects = [f.project('prj_original', f.alias)]
  fake.handlers['project.list.request'] = () => ({ payload: { projects } })
  await makeTree(node, subdir)
  assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_original')
  await call(node, 'session.start', { cwd: subdir, launcher: 'my-codex' })
  assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_original')
  await makeTree(node, clone)
  assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, undefined)
  projects.push(f.project('prj_clone', clone))
  await makeTree(node, clone)
  assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_clone')
  await call(node, 'session.start', { cwd: clone, launcher: 'my-codex' })
  assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_clone')
})

test('creation reads changed symlinks again instead of reusing the display path cache', async (t) => {
  const f = projectFixture(t)
  const { fake, node } = await setup(t)
  fake.handlers['project.list.request'] = () => ({ payload: { projects: [f.project('prj_original', f.alias)] } })
  await makeTree(node, f.repo)
  assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, 'prj_original')
  rmSync(f.alias)
  const other = join(f.dir, 'other')
  mkdirSync(other)
  symlinkSync(other, f.alias)
  await makeTree(node, f.repo)
  assert.equal(fake.requestsOf('workspace.create.request').at(-1).source.projectId, undefined)
})

test('a direct start does not create an Agent when its explicitly owned directory workspace fails', async (t) => {
  const f = projectFixture(t)
  const { fake, node } = await setup(t)
  fake.handlers['project.list.request'] = () => ({ payload: { projects: [f.project('prj_original', f.alias)] } })
  fake.handlers['workspace.create.request'] = () => ({ payload: { workspace: null, error: 'Project was archived' } })
  await assert.rejects(call(node, 'session.start', { cwd: f.repo, launcher: 'my-codex' }), /Project was archived/)
  assert.equal(fake.requestsOf('agent.create.request').length, 0)
})

test('a managed start requires the prepared directory before creating an agent', async (t) => {
  const f = projectFixture(t)
  const { fake, node } = await setup(t)
  fake.handlers['project.list.request'] = () => ({ payload: { projects: [f.project('prj_original', f.alias)] } })
  const make = (path) => workspaceDescriptor({ workspaceDirectory: path, projectId: 'prj_original', workspaceKind: 'directory' })
  fake.handlers['workspace.create.request'] = () => ({ payload: { workspace: make(f.dir) } })
  const params = { cwd: f.repo, launcher: 'my-codex', prompt: 'Check the repository', backend_workspace_root: true }
  await assert.rejects(call(node, 'session.start', params), /prepared task directory/)
  assert.equal(fake.requestsOf('agent.create.request').length, 0)
  fake.handlers['workspace.create.request'] = () => ({ payload: { workspace: make(f.repo) } })
  const result = await call(node, 'session.start', params)
  assert.equal(result.session.cwd, f.repo)
  assert.equal(fake.requestsOf('agent.create.request').at(-1).config.cwd, f.repo)
  assert.equal(fake.requestsOf('agent.create.request').at(-1).initialPrompt, undefined)
  assert.equal(fake.requestsOf('send_agent_message_request').at(-1).text, params.prompt)
})

test('a directory workspace whose reply was lost uses the same creation key before starting one Agent', async (t) => {
  const f = projectFixture(t)
  const { fake, node } = await setup(t)
  fake.handlers['project.list.request'] = () => ({ payload: { projects: [f.project('prj_original', f.alias)] } })
  const made = workspaceDescriptor({ workspaceDirectory: f.repo, projectId: 'prj_original', workspaceKind: 'directory' })
  let first
  fake.handlers['workspace.create.request'] = (msg, c) => {
    if (!first) {
      first = msg
      fake.upsertWorkspace(made)
      c.socket.destroy()
      return null
    }
    assert.equal(msg.idempotencyKey, first.idempotencyKey)
    assert.deepEqual(msg.source, first.source)
    return { payload: { workspace: made } }
  }
  await call(node, 'session.start', { cwd: f.repo, launcher: 'my-codex' })
  assert.equal(fake.requestsOf('workspace.create.request').length, 2)
  assert.equal(fake.requestsOf('agent.create.request').length, 1)
  assert.equal(fake.requestsOf('agent.create.request')[0].workspaceId, made.id)
})

test('a slow directory workspace keeps the pending start visible and its later failure is reported', async (t) => {
  const f = projectFixture(t)
  const { fake, node, rows } = await setup(t)
  fake.handlers['project.list.request'] = () => ({ payload: { projects: [f.project('prj_original', f.alias)] } })
  let finish
  fake.handlers['workspace.create.request'] = (msg, c) => {
    finish = () => c.write(JSON.stringify({ type: 'session', message: { type: 'workspace.create.response',
      payload: { requestId: msg.requestId, workspace: null, error: 'Project was archived' } } }))
    return null
  }
  const result = await call(node, 'session.start', { cwd: f.repo, launcher: 'my-codex' })
  assert.equal(result.session.starting, true)
  finish()
  await until('the failed workspace start remains visible', () => rows().find(r => r.session_id === result.session.session_id)?.start_error?.includes('Project was archived'))
  assert.equal(fake.requestsOf('agent.create.request').length, 0)
})

// ---- renaming and archiving Paseo's workspaces (#6297) ----

test("a workspace's title and a project's name are set in Paseo, and the rows say them at once; nothing else is renamed", async (t) => {
  const dir = realpathSync(mkdtempSync('/tmp/ppaseo-ws-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const w = repoWorkspaces(dir)
  const { fake, node, rows } = await setup(t, { workspaces: [w.main, w.wt], projects: ['prj_1'] })
  await until('the workspaces', () => node.last('runtime.workspaces'))
  const inTree = agentSnapshot({ provider: 'my-codex', cwd: w.tree, title: 'In the tree', workspaceId: w.wt.id })
  fake.upsert(inTree, w.wt.project)
  await until('listed in its workspace', () => rows().some((r) => r.session_id === `paseo-${inTree.id}` && r.slot_name === 'Login fix'))

  const renamed = await call(node, 'workspace.rename', { workspace_id: w.wt.id, title: ' Login work ' })
  assert.deepEqual(fake.renamed.at(-1), ['workspace', w.wt.id, 'Login work'])
  assert.equal(fake.requestsOf('workspace.title.set.request').at(-1).title, 'Login work')
  assert.equal(renamed.workspace.title, 'Login work')
  await until("the agent's row", () => rows().find((r) => r.session_id === `paseo-${inTree.id}`)?.slot_name === 'Login work')
  await until('the list', () => node.last('runtime.workspaces').workspaces.find((x) => x.workspace_id === w.wt.id)?.title === 'Login work')

  // The project of the agents set up in prj_1: Paseo sends no agent of it again, the rows still say the new name.
  assert.deepEqual(await call(node, 'workspace.rename', { space_id: 'prj_1', title: 'Renamed' }), { space_name: 'Renamed' })
  assert.deepEqual(fake.requestsOf('project.rename.request').at(-1).customName, 'Renamed')
  await until('every row of the project', () => rows().filter((r) => r.space_id === 'prj_1').every((r) => r.space_name === 'Renamed'))
  assert.ok(rows().filter((r) => r.space_id === 'prj_1').length >= 3)
  assert.ok(rows().filter((r) => r.space_id === 'prj_app').every((r) => r.space_name === 'app'), 'another project keeps its name')

  for (const params of [{ workspace_id: 'wks_00000000000000ff', title: 'x' }, { space_id: 'prj_none', title: 'x' }]) {
    await assert.rejects(call(node, 'workspace.rename', params), (err) => err.code === 'workspace_missing')
  }
  await assert.rejects(call(node, 'workspace.rename', { workspace_id: w.wt.id, title: '  ' }), (err) => err.code === 'bad_request')
  fake.handlers['workspace.title.set.request'] = (msg) => ({ payload: { workspaceId: msg.workspaceId, accepted: false, title: null, error: 'Workspace not found' } })
  await assert.rejects(call(node, 'workspace.rename', { workspace_id: w.wt.id, title: 'x' }), (err) => err.code === 'rename_failed' && /Workspace not found/.test(err.message))
})

test('a workspace archived in Paseo leaves the list with its agents; one Paseo does not have, or will not archive, is an error', async (t) => {
  const dir = realpathSync(mkdtempSync('/tmp/ppaseo-ws-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const w = repoWorkspaces(dir)
  const { fake, node, rows } = await setup(t, { workspaces: [w.main, w.wt] })
  await until('the workspaces', () => node.last('runtime.workspaces'))
  const inTree = agentSnapshot({ provider: 'my-codex', cwd: w.tree, title: 'In the tree', workspaceId: w.wt.id })
  fake.upsert(inTree, w.wt.project)
  await until('listed', () => rows().some((r) => r.session_id === `paseo-${inTree.id}`))
  let removed
  fake.onArchiveWorkspace = (d) => { removed = d.workspaceDirectory }
  const res = await call(node, 'workspace.archive', { workspace_id: w.wt.id })
  assert.match(res.archived_at, /^\d{4}-/)
  assert.deepEqual(fake.requestsOf('archive_workspace_request').map((r) => r.workspaceId), [w.wt.id])
  assert.equal(removed, w.tree)
  await until('off the list', () => !node.last('runtime.workspaces').workspaces.some((x) => x.workspace_id === w.wt.id))
  await until('its agent archived with it', () => !rows().some((r) => r.session_id === `paseo-${inTree.id}`))
  assert.ok(bridgeHas(node, w.main.id), 'the checkout stays')

  await assert.rejects(call(node, 'workspace.archive', { workspace_id: w.wt.id }), (err) => err.code === 'workspace_missing')
  fake.onArchiveWorkspace = () => { throw new Error('Refusing to delete non-Paseo worktree') }
  await assert.rejects(call(node, 'workspace.archive', { workspace_id: w.main.id }), (err) => err.code === 'archive_failed' && /non-Paseo worktree/.test(err.message))
})

function bridgeHas(node, id) {
  return node.last('runtime.workspaces').workspaces.some((x) => x.workspace_id === id)
}


test('Paseo 0.16 native jobs are read without a watch and retain their parent activity', async (t) => {
  const inventory = new Map()
  const { fake, a, rows } = await setup(t, { version: '0.16.0', features: { agentBackgroundWork: true },
    handlers: { 'agent.background_work.list.request': (m) => ({ payload: { agentId: m.agentId, items: inventory.get(m.agentId) ?? [], error: null } }) } })
  const job = { id: 'native-shell', kind: 'shell', description: 'Build the project', command: 'make build', startedAt: '2026-11-16T00:00:00Z' }
  inventory.set(a.codex.id, [job])
  fake.update(a.codex.id, { status: 'idle', requiresAttention: true, backgroundWork: { count: 1 },
    workspaceId: a.claude.workspaceId, labels: { 'paseo.parent-agent-id': a.claude.id } })
  await until('native work and its parent listed', () => rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.background?.[0]?.id === job.id &&
    rows().find((r) => r.session_id === SID)?.background?.some((b) => b.id === `paseo-${a.codex.id}`))
  const listed = rows().find((r) => r.session_id === `paseo-${a.codex.id}`)
  assert.deepEqual(listed.background, [{ id: job.id, kind: 'shell', description: job.description, command: job.command, started_at: new Date(job.startedAt).toISOString() }])
  assert.equal(fake.requestsOf('agent.timeline.set_subscription.request').length, 0)
  const subscribed = fake.requestsOf('session.events.set_subscription.request')[0]
  assert.ok(subscribed.events.includes('agent.background_work.update'))
  fake.emit('events', { type: 'agent.background_work.update', payload: { agentId: a.codex.id, items: [] } }, (s) => s.events.includes('agent.background_work.update'))
  await until('finished background child no longer keeps parent busy', () => !rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.background && !rows().find((r) => r.session_id === SID)?.background)
})

test('a positive native summary stays working through read failure and a sweep repairs it', async (t) => {
  let fail = true
  const { fake, a, rows } = await setup(t, { version: '0.16.0', features: { agentBackgroundWork: true }, sweepMs: 25,
    handlers: { 'agent.background_work.list.request': (m) => ({ payload: { agentId: m.agentId,
      items: fail ? [] : [{ id: 'restored-job', kind: 'monitor', description: 'Wait for deployment', startedAt: '2026-11-16T00:00:00Z' }], error: fail ? 'Inventory unavailable' : null } }) } })
  fake.update(a.codex.id, { status: 'idle', requiresAttention: true, backgroundWork: { count: 1 } })
  await until('failed detail read recorded', () => fake.requestsOf('agent.background_work.list.request').length > 0)
  assert.equal(rows().find((r) => r.session_id === `paseo-${a.codex.id}`).status, 'working')
  fail = false
  await until('sweep repairs native inventory', () => rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.background?.[0]?.id === 'restored-job')
  fake.update(a.codex.id, { status: 'closed', backgroundWork: undefined })
  await until('unloaded inventory clears', () => !rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.background)
  assert.deepEqual(fake.loads, [])
})

test('a reconnect refetches native job identities even when the summary count stays the same', async (t) => {
  let job = 'before-reconnect'
  const { fake, a, rows } = await setup(t, { version: '0.16.0', features: { agentBackgroundWork: true },
    handlers: { 'agent.background_work.list.request': (m) => ({ payload: { agentId: m.agentId,
      items: [{ id: job, kind: 'shell', description: null, startedAt: '2026-11-16T00:00:00Z' }], error: null } }) } })
  fake.update(a.codex.id, { status: 'idle', backgroundWork: { count: 1 } })
  await until('initial native inventory', () => rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.background?.[0]?.id === job)
  job = 'after-reconnect'
  fake.drop()
  await until('reconnect replaces same-count inventory', () => rows().find((r) => r.session_id === `paseo-${a.codex.id}`)?.background?.[0]?.id === job)
})

test('initial Paseo 0.16 discovery reads existing native jobs after subscribing to updates', async (t) => {
  const existing = Array.from({ length: 4 }, (_, n) => agentSnapshot({ provider: 'my-codex', cwd: '/tmp', status: 'idle',
    backgroundWork: { count: n === 0 ? 1 : 0 } }))
  const { fake, rows } = await setup(t, { version: '0.16.0', features: { agentBackgroundWork: true },
    agents: existing.map((agent) => ({ agent, project: PROJECT })),
    handlers: { 'agent.background_work.list.request': (m) => ({ payload: { agentId: m.agentId,
      items: [{ id: 'already-running', kind: 'shell', description: null, startedAt: '2026-11-16T00:00:00Z' }], error: null } }) } })
  await until('existing work appears on initial discovery', () => rows().find((r) => r.session_id === `paseo-${existing[0].id}`)?.background?.[0]?.id === 'already-running')
  assert.equal(fake.requestsOf('agent.background_work.list.request').length, 1)
  const subscription = fake.requests.findIndex((r) => r.type === 'session.events.set_subscription.request')
  const list = fake.requests.findIndex((r) => r.type === 'agent.background_work.list.request')
  assert.ok(subscription >= 0 && subscription < list)
})

test("Paseo pin changes reach the catalog without an agent update, survive reconnect, and never write preferences", async (t) => {
  const pinnedAt = '2026-11-16T12:00:00.000Z'
  const pinned = workspaceDescriptor({ id: 'wks_native_pin', workspaceDirectory: '/tmp', pinnedAt })
  const sibling = { ...pinned, id: 'wks_native_sibling', pinnedAt: null }
  const { fake, node, bridge } = await setup(t, { workspaces: [pinned, sibling] })
  const rows = () => node.last('runtime.workspaces')?.workspaces ?? []
  const row = (id) => rows().find((w) => w.workspace_id === id)
  await until('the native pin listed', () => row(pinned.id)?.pinned_at === pinnedAt)
  assert.equal(row(sibling.id).pinned_at, undefined)
  fake.upsertWorkspace({ ...pinned, pinnedAt: null })
  await until('the native pin cleared', () => row(pinned.id) && !row(pinned.id).pinned_at)
  fake.upsertWorkspace({ ...sibling, pinnedAt })
  await until('only the sibling pinned', () => row(sibling.id)?.pinned_at === pinnedAt)
  // The reconnect page is already stale when its subscription delivers an unpin. A full read cannot resurrect it.
  fake.duringPage = (msg) => {
    if (msg.type !== 'fetch_workspaces_request' || !msg.subscribe) return
    fake.duringPage = undefined
    fake.upsertWorkspace(sibling)
  }
  fake.drop()
  await until('reconnected and the stale pin discarded', () => fake.hellos.length === 2
    && fake.requestsOf('fetch_workspaces_request').length === 2 && row(sibling.id) && !row(sibling.id).pinned_at)
  assert.equal(bridge.state.workspaces.get(sibling.id).pinnedAt, null)
  fake.upsertWorkspace({ ...pinned, pinnedAt })
  await until('pinned again', () => row(pinned.id)?.pinned_at === pinnedAt)
  fake.removeWorkspace(pinned.id)
  await until('removed pinned workspace gone', () => !row(pinned.id))
  assert.equal(fake.requestsOf('workspace.pin.set.request').length, 0, 'catalog reads never change the source pin')
})

test("a native pinned workspace keeps old conversations beyond the list limit, including a delegated agent's parents", async (t) => {
  const workspace = workspaceDescriptor({ id: 'wks_old_pin', workspaceDirectory: '/tmp', pinnedAt: null })
  const { fake, node, rows } = await setup(t, { workspaces: [workspace], listMax: 4 })
  const old = (id, extra = {}) => agentSnapshot({ id, provider: 'my-codex', cwd: '/tmp', status: 'closed', updatedAt: '2020-01-01T00:00:00Z', ...extra })
  const parent = old('old-parent', { workspaceId: workspace.id })
  const child = old('old-child', { workspaceId: workspace.id, labels: { 'paseo.parent-agent-id': parent.id } })
  fake.upsert(parent, PROJECT)
  fake.upsert(child, PROJECT)
  fake.upsertWorkspace({ ...workspace, pinnedAt: '2026-11-16T12:00:00.000Z' })
  await until('the pinned conversation and its parent retained', () => rows().some((r) => r.session_id === 'paseo-old-child'))
  assert.ok(rows().some((r) => r.session_id === 'paseo-old-parent'))
  assert.equal(rows().find((r) => r.session_id === 'paseo-old-child').parent_session_id, 'paseo-old-parent')
  fake.upsertWorkspace(workspace)
  await until('unpin releases the old conversations', () => !rows().some((r) => r.session_id === 'paseo-old-child'))
  assert.equal(node.last('runtime.workspaces').workspaces.find((w) => w.workspace_id === workspace.id).pinned_at, undefined)
})

test('read-only (plan) start and takeover fail closed on missing mode, initialPrompt, dual-agent, pendingReadonly bypasses, and 0..callWait races', async (t) => {
  const { fake, node, bridge, rows } = await setup(t, { callWait: 80 })
  const plan = (params) => call(node, 'session.start', { cwd: '/tmp', prompt: 'inspect only', permission_mode: 'plan', ...params })

  // 1. Synchronous start with missing/null/empty/uppercase currentModeId must withhold initialPrompt, archive, and refuse.
  for (const mode of [undefined, null, '', 'PLAN']) {
    const sendsBefore = fake.requestsOf('send_agent_message_request').length
    fake.handlers['agent.create.request'] = (msg) => ({
      payload: { agent: agentSnapshot({ id: msg.agentId, provider: 'my-claude', cwd: '/tmp', currentModeId: mode }) },
    })
    const err = await plan({ launcher: 'my-claude' }).catch((e) => e)
    assert.equal(err.code, 'readonly_unsupported', `sync start mode=${String(mode)}`)
    const req = fake.requestsOf('agent.create.request').at(-1)
    assert.equal(req.initialPrompt, undefined, 'readonly start must not send initialPrompt before mode is verified')
    assert.equal(fake.requestsOf('send_agent_message_request').length, sendsBefore, 'prompt must never execute on non-plan agent')
    assert.equal(fake.requestsOf('archive_agent_request').at(-1).agentId, req.agentId)
    assert.equal(rows().some((r) => r.session_id === `paseo-${req.agentId}`), false)
  }

  // 1b. Verified plan start withholds initialPrompt in create and delivers it only after plan mode is confirmed.
  const sendsBeforeOk = fake.requestsOf('send_agent_message_request').length
  fake.handlers['agent.create.request'] = (msg) => ({
    payload: { agent: agentSnapshot({ id: msg.agentId, provider: 'my-claude', cwd: '/tmp', currentModeId: 'plan', labels: msg.labels }) },
  })
  const okPlan = await plan({ launcher: 'my-claude', prompt: 'verified readonly prompt' })
  assert.equal(fake.requestsOf('agent.create.request').at(-1).initialPrompt, undefined)
  assert.equal(fake.requestsOf('send_agent_message_request').length, sendsBeforeOk + 1)
  assert.equal(fake.requestsOf('send_agent_message_request').at(-1).text, 'verified readonly prompt')
  assert.equal(okPlan.session.permission_mode, 'plan')

  // 2. Premature non-plan agent_update during late readonly start (BEFORE session.send and BEFORE response):
  // pendingReadonly must archive it, keep starting:true, keep send queued, and never deliver to the writable agent.
  let createMsg
  let finishCreate
  fake.handlers['agent.create.request'] = (msg, c) => {
    createMsg = msg
    finishCreate = () => answerWith(c, 'agent.create.response', {
      requestId: msg.requestId,
      agent: agentSnapshot({ id: msg.agentId, provider: 'my-claude', cwd: '/tmp', currentModeId: null, labels: msg.labels }),
    })
    return null
  }
  const late = await plan({ launcher: 'my-claude', session_id: 'plan-late-1', prompt: 'never execute' })
  assert.equal(late.session.starting, true)
  assert.equal(createMsg.initialPrompt, undefined)
  const archivesBeforePush = fake.requestsOf('archive_agent_request').length
  // (a) Push premature non-plan update WITH empty string label "" (tests empty-string ?? bypass fix).
  fake.upsert(agentSnapshot({
    id: createMsg.agentId, provider: 'my-claude', cwd: '/tmp', currentModeId: 'default',
    labels: { 'persona.session': '' },
  }), PROJECT)
  await until('premature non-plan update archived by pendingReadonly', () => fake.requestsOf('archive_agent_request').length > archivesBeforePush)
  assert.equal(bridge.state.agents.has(createMsg.agentId), false)
  assert.equal(rows().find((r) => r.session_id === 'plan-late-1')?.starting, true)
  // (b) Push a 'plan' update then downgrade it to 'default' while still pending: must evict stale 'plan' entry!
  const planFirst = agentSnapshot({
    id: createMsg.agentId, provider: 'my-claude', cwd: '/tmp', currentModeId: 'plan',
    labels: createMsg.labels, updatedAt: new Date(Date.now() + 1000).toISOString(),
  })
  fake.upsert(planFirst, PROJECT)
  await until('plan update indexed temporarily', () => bridge.state.agents.has(createMsg.agentId))
  fake.upsert({ ...planFirst, currentModeId: 'default', updatedAt: new Date(Date.now() + 2000).toISOString() }, PROJECT)
  await until('downgraded plan->default evicted while pending', () => !bridge.state.agents.has(createMsg.agentId))
  const sendsBeforeLate = fake.requestsOf('send_agent_message_request').length
  const queued = await call(node, 'session.send', { session_id: 'plan-late-1', text: 'do not deliver', client_msg_id: 'cm-plan-late' })
  assert.deepEqual(queued, { ok: true, queued: true })
  finishCreate()
  await until('late readonly start marked failed', () => rows().find((r) => r.session_id === 'plan-late-1')?.start_error)
  assert.equal(fake.requestsOf('send_agent_message_request').length, sendsBeforeLate, 'writable agent must never receive queued message or prompt')

  // 3. Dual live agents sharing a Claude session (older plan + newer default): readonly takeOver must validate active receiver!
  const sharedClaude = 'bbbbbbbb-0000-4000-8000-000000000201'
  const olderPlan = agentSnapshot({
    id: 'dual-old-plan', provider: 'my-claude', cwd: '/tmp', status: 'idle', currentModeId: 'plan',
    createdAt: '2026-11-20T00:00:00.000Z', persistence: { provider: 'my-claude', sessionId: sharedClaude },
  })
  const newerWritable = agentSnapshot({
    id: 'dual-new-default', provider: 'my-claude', cwd: '/tmp', status: 'idle', currentModeId: 'default',
    createdAt: '2026-11-21T00:00:00.000Z', persistence: { provider: 'my-claude', sessionId: sharedClaude },
  })
  fake.upsert(olderPlan, PROJECT)
  fake.upsert(newerWritable, PROJECT)
  await until('newer writable agent wins bySession', () => bridge.state.bySession.get(sharedClaude) === 'dual-new-default')
  const dualErr = await call(node, 'session.start', { cwd: '/tmp', resume: sharedClaude, permission_mode: 'plan' }).catch((e) => e)
  assert.equal(dualErr.code, 'readonly_unsupported', 'must check active receiver, not older plan agent')

  // 4. Takeover of archived plan agent whose import returns non-plan or null: archive, refuse, and invalidate stale archived mode.
  for (const [idx, mode] of ['default', null].entries()) {
    const archTranscript = `aaaaaaaa-0000-4000-8000-00000000010${idx + 1}`
    const archSid = `paseo-arch-plan-${idx + 1}`
    const archAgent = agentSnapshot({
      id: `arch-plan-${idx + 1}`, provider: 'my-claude', cwd: '/tmp', status: 'idle', currentModeId: 'plan',
      persistence: { provider: 'my-claude', sessionId: archTranscript }, labels: { 'persona.session': archSid },
    })
    fake.upsert(archAgent, PROJECT)
    await until('live plan agent listed', () => rows().some((r) => r.session_id === archSid && r.status === 'idle'))
    fake.archive(archAgent.id)
    await until('archived plan agent cached', () => rows().find((r) => r.session_id === archSid)?.status === 'offline')
    fake.handlers.import_agent_request = (msg) => ({
      type: 'status',
      payload: {
        status: 'agent_resumed',
        agent: agentSnapshot({
          id: `resumed-${idx + 1}`, provider: 'my-claude', cwd: '/tmp', currentModeId: mode,
          persistence: { provider: 'my-claude', sessionId: archTranscript }, labels: msg.labels,
        }),
      },
    })
    const err = await call(node, 'session.start', { cwd: '/tmp', resume: archSid, permission_mode: 'plan' }).catch((e) => e)
    assert.equal(err.code, 'readonly_unsupported', `takeover resumed mode=${String(mode)}`)
    assert.equal(fake.requestsOf('archive_agent_request').at(-1).agentId, `resumed-${idx + 1}`)
    // Stale archived mode invalidated: immediate retry fails before calling import_agent_request again.
    const importsAfter = fake.requestsOf('import_agent_request').length
    const cachedErr = await call(node, 'session.start', { cwd: '/tmp', resume: archSid, permission_mode: 'plan' }).catch((e) => e)
    assert.equal(cachedErr.code, 'readonly_unsupported')
    assert.equal(fake.requestsOf('import_agent_request').length, importsAfter)
  }

  // 5. Late readonly takeover + unlabelled sharedId premature non-plan update + no auto-retry.
  const archTranscript3 = 'aaaaaaaa-0000-4000-8000-000000000103'
  const archSid3 = 'paseo-arch-plan-3'
  const archAgent3 = agentSnapshot({
    id: 'arch-plan-3', provider: 'my-claude', cwd: '/tmp', status: 'idle', currentModeId: 'plan',
    persistence: { provider: 'my-claude', sessionId: archTranscript3 }, labels: { 'persona.session': archSid3 },
  })
  fake.upsert(archAgent3, PROJECT)
  await until('third live plan agent listed', () => rows().some((r) => r.session_id === archSid3 && r.status === 'idle'))
  fake.archive(archAgent3.id)
  await until('third archived plan agent cached', () => rows().find((r) => r.session_id === archSid3)?.status === 'offline')
  let finishLateImport
  fake.handlers.import_agent_request = (msg, c) => {
    finishLateImport = () => answerWith(c, 'status', {
      status: 'agent_resumed', requestId: msg.requestId,
      agent: agentSnapshot({
        id: 'resumed-late-bad', provider: 'my-claude', cwd: '/tmp', currentModeId: 'default',
        persistence: { provider: 'my-claude', sessionId: archTranscript3 }, labels: msg.labels,
      }),
    })
    return null
  }
  const lateTake = await call(node, 'session.start', { cwd: '/tmp', resume: archSid3, permission_mode: 'plan' })
  assert.equal(lateTake.session.starting, true)
  // Unlabelled agent_update whose persistence.sessionId resolves via sharedId to archSid3 must be blocked!
  const archivesBeforeUnlabelled = fake.requestsOf('archive_agent_request').length
  fake.upsert(agentSnapshot({
    id: 'unlabelled-bad-import', provider: 'my-claude', cwd: '/tmp', currentModeId: 'default',
    persistence: { provider: 'my-claude', sessionId: archTranscript3 },
  }), PROJECT)
  await until('unlabelled sharedId non-plan update archived', () => fake.requestsOf('archive_agent_request').length > archivesBeforeUnlabelled)
  assert.equal(bridge.state.bySession.has(archSid3), false)
  const sendsBeforeTake = fake.requestsOf('send_agent_message_request').length
  const queuedTake = await call(node, 'session.send', { session_id: archSid3, text: 'do not deliver takeover', client_msg_id: 'cm-take-late' })
  assert.deepEqual(queuedTake, { ok: true, queued: true })
  const importsBefore = fake.requestsOf('import_agent_request').length
  finishLateImport()
  await until('late readonly takeover marked failed', () => rows().find((r) => r.session_id === archSid3)?.start_error)
  assert.equal(fake.requestsOf('send_agent_message_request').length, sendsBeforeTake)
  const retryErr = await call(node, 'session.send', { session_id: archSid3, text: 'after fail', client_msg_id: 'cm-take-after' }).catch((e) => e)
  assert.equal(retryErr.code, 'start_failed')
  assert.equal(fake.requestsOf('import_agent_request').length, importsBefore, 'failed readonly takeover must not auto-retry')

  // 6. 0..callWait blind-spot race: concurrent readonly takeover during pre-timeout writable takeover must fail closed.
  const archTranscript4 = 'aaaaaaaa-0000-4000-8000-000000000104'
  const archSid4 = 'paseo-arch-plan-4'
  const archAgent4 = agentSnapshot({
    id: 'arch-plan-4', provider: 'my-claude', cwd: '/tmp', status: 'idle', currentModeId: 'plan',
    persistence: { provider: 'my-claude', sessionId: archTranscript4 }, labels: { 'persona.session': archSid4 },
  })
  fake.upsert(archAgent4, PROJECT)
  await until('fourth live plan agent listed', () => rows().some((r) => r.session_id === archSid4 && r.status === 'idle'))
  fake.archive(archAgent4.id)
  await until('fourth archived plan agent cached', () => rows().find((r) => r.session_id === archSid4)?.status === 'offline')
  let finishImport4
  fake.handlers.import_agent_request = (msg, c) => {
    finishImport4 = () => answerWith(c, 'status', {
      status: 'agent_resumed', requestId: msg.requestId,
      agent: agentSnapshot({
        id: 'resumed-writable-4', provider: 'my-claude', cwd: '/tmp', currentModeId: 'default',
        persistence: { provider: 'my-claude', sessionId: archTranscript4 }, labels: msg.labels,
      }),
    })
    return null
  }
  const writablePromise = call(node, 'session.start', { cwd: '/tmp', resume: archSid4 })
  await until('writable import sent before callWait expires', () => Boolean(finishImport4))
  // Arrives BEFORE callWait expires (0..callWait window): must STILL reject!
  const raceReadonlyErr = await call(node, 'session.start', { cwd: '/tmp', resume: archSid4, permission_mode: 'plan' }).catch((e) => e)
  assert.equal(raceReadonlyErr.code, 'readonly_unsupported')
  // Also wait until writablePromise times out to inFlight, and verify post-timeout readonly join STILL rejects.
  const writableStart = await writablePromise
  assert.equal(writableStart.session.starting, true)
  const postTimeoutErr = await call(node, 'session.start', { cwd: '/tmp', resume: archSid4, permission_mode: 'plan' }).catch((e) => e)
  assert.equal(postTimeoutErr.code, 'readonly_unsupported')
  finishImport4()
  await until('writable import finishes cleanly', () => rows().some((r) => r.session_id === archSid4 && !r.starting))

  // 7. Reverse direction: writable takeover arriving while a readonly takeover is in flight waits and succeeds writable.
  const archTranscript5 = 'aaaaaaaa-0000-4000-8000-000000000105'
  const archSid5 = 'paseo-arch-plan-5'
  const archAgent5 = agentSnapshot({
    id: 'arch-plan-5', provider: 'my-claude', cwd: '/tmp', status: 'idle', currentModeId: 'plan',
    persistence: { provider: 'my-claude', sessionId: archTranscript5 }, labels: { 'persona.session': archSid5 },
  })
  fake.upsert(archAgent5, PROJECT)
  await until('fifth live plan agent listed', () => rows().some((r) => r.session_id === archSid5 && r.status === 'idle'))
  fake.archive(archAgent5.id)
  await until('fifth archived plan agent cached', () => rows().find((r) => r.session_id === archSid5)?.status === 'offline')
  let importCount5 = 0
  let finishReadonly5
  fake.handlers.import_agent_request = (msg, c) => {
    importCount5 += 1
    if (importCount5 === 1) {
      finishReadonly5 = () => answerWith(c, 'status', {
        status: 'agent_resumed', requestId: msg.requestId,
        agent: agentSnapshot({
          id: 'resumed-readonly-fail-5', provider: 'my-claude', cwd: '/tmp', currentModeId: 'default',
          persistence: { provider: 'my-claude', sessionId: archTranscript5 }, labels: msg.labels,
        }),
      })
      return null
    }
    return {
      type: 'status',
      payload: {
        status: 'agent_resumed',
        agent: agentSnapshot({
          id: 'resumed-writable-ok-5', provider: 'my-claude', cwd: '/tmp', currentModeId: 'default',
          persistence: { provider: 'my-claude', sessionId: archTranscript5 }, labels: msg.labels,
        }),
      },
    }
  }
  const readonly5 = await call(node, 'session.start', { cwd: '/tmp', resume: archSid5, permission_mode: 'plan' })
  assert.equal(readonly5.session.starting, true)
  const writable5 = call(node, 'session.start', { cwd: '/tmp', resume: archSid5 })
  // FIFO barrier on the same RPC connection ensures writable5 has entered takeOver and parked on readonly5.
  await call(node, 'session.start', { cwd: '/tmp', resume: 'cccccccc-0000-4000-8000-000000000999', permission_mode: 'plan' }).catch(() => {})
  finishReadonly5()
  const writable5Res = await writable5
  assert.equal(writable5Res.session.session_id, archSid5)
  assert.equal(writable5Res.session.starting, undefined)
  assert.equal(importCount5, 2, 'writable takeover must run its own import after readonly fails')

  // 8. [CRITICAL-1] Writable takeOver while readonly start is awaiting deliverMessage must NOT livelock;
  //    AND [CRITICAL-2A] concurrent session.send during readonly start delivers strictly AFTER initialPrompt.
  let finishPrompt8
  const sentTexts8 = []
  fake.handlers['agent.create.request'] = (msg) => ({
    payload: { agent: agentSnapshot({ id: msg.agentId, provider: 'my-claude', cwd: '/tmp', currentModeId: 'plan', labels: msg.labels }) },
  })
  fake.handlers.send_agent_message_request = (msg, c) => {
    sentTexts8.push(msg.text)
    if (msg.text === 'initial readonly prompt 8') {
      finishPrompt8 = () => answerWith(c, 'send_agent_message_response', { requestId: msg.requestId, agentId: msg.agentId, accepted: true, error: null })
      return null
    }
    return undefined
  }
  const start8 = plan({ launcher: 'my-claude', session_id: 'plan-order-8', prompt: 'initial readonly prompt 8', client_msg_id: 'cm-init-8' })
  await until('initialPrompt sent and held open', () => Boolean(finishPrompt8))
  // Both a concurrent writable takeOver AND a concurrent session.send arrive while start8 is awaiting deliverMessage:
  const takeover8 = call(node, 'session.start', { cwd: '/tmp', resume: 'plan-order-8' })
  const followUp8 = call(node, 'session.send', { session_id: 'plan-order-8', text: 'follow-up 8', client_msg_id: 'cm-follow-8' })
  // FIFO barrier confirms both entered bridge before releasing initialPrompt.
  await call(node, 'session.start', { cwd: '/tmp', resume: 'cccccccc-0000-4000-8000-000000000998', permission_mode: 'plan' }).catch(() => {})
  assert.deepEqual(sentTexts8, ['initial readonly prompt 8'], 'follow-up send must wait until initialPrompt finishes')
  finishPrompt8()
  const [start8Res, takeover8Res, followUp8Res] = await Promise.all([start8, takeover8, followUp8])
  assert.equal(start8Res.session.session_id, 'plan-order-8')
  assert.equal(takeover8Res.session.session_id, 'plan-order-8')
  assert.equal(followUp8Res.ok, true)
  assert.deepEqual(sentTexts8, ['initial readonly prompt 8', 'follow-up 8'])
  assert.equal(bridge.state.echoes.has(uuidOf(`persona:${fake.requestsOf('agent.create.request').at(-1).agentId}:cm-init-8`)), true)
  delete fake.handlers.send_agent_message_request

  // 9. [CRITICAL-2A] 0..callWait session.send during a readonly takeOver on an archived agent must NOT wake it when import rejects!
  const archTranscript6 = 'aaaaaaaa-0000-4000-8000-000000000106'
  const archSid6 = 'paseo-arch-plan-6'
  const archAgent6 = agentSnapshot({
    id: 'arch-plan-6', provider: 'my-claude', cwd: '/tmp', status: 'idle', currentModeId: 'plan',
    persistence: { provider: 'my-claude', sessionId: archTranscript6 }, labels: { 'persona.session': archSid6 },
  })
  fake.upsert(archAgent6, PROJECT)
  await until('sixth live plan agent listed', () => rows().some((r) => r.session_id === archSid6 && r.status === 'idle'))
  fake.archive(archAgent6.id)
  await until('sixth archived plan agent cached', () => rows().find((r) => r.session_id === archSid6)?.status === 'offline')
  let finishImport6
  fake.handlers.import_agent_request = (msg, c) => {
    finishImport6 = () => answerWith(c, 'status', {
      status: 'agent_resumed', requestId: msg.requestId,
      agent: agentSnapshot({
        id: 'resumed-bad-6', provider: 'my-claude', cwd: '/tmp', currentModeId: 'default',
        persistence: { provider: 'my-claude', sessionId: archTranscript6 }, labels: msg.labels,
      }),
    })
    return null
  }
  const sendsBefore6 = fake.requestsOf('send_agent_message_request').length
  const roTake6 = call(node, 'session.start', { cwd: '/tmp', resume: archSid6, permission_mode: 'plan' })
  await until('readonly import 6 sent before callWait', () => Boolean(finishImport6))
  // Arrives BEFORE callWait expires (0..callWait window): must wait on pendingOps, NOT wake archived agent!
  const send6 = call(node, 'session.send', { session_id: archSid6, text: 'must not wake archived', client_msg_id: 'cm-wake-6' })
  await call(node, 'session.start', { cwd: '/tmp', resume: 'cccccccc-0000-4000-8000-000000000997', permission_mode: 'plan' }).catch(() => {})
  assert.equal(fake.requestsOf('send_agent_message_request').length, sendsBefore6, 'must not wake archived agent while readonly takeOver is pending')
  finishImport6()
  const [roTake6Err, send6Err] = await Promise.all([roTake6.catch((e) => e), send6.catch((e) => e)])
  assert.equal(roTake6Err.code, 'readonly_unsupported')
  assert.equal(send6Err.code, 'start_failed')
  assert.equal(fake.requestsOf('send_agent_message_request').length, sendsBefore6)

  // 10. [CRITICAL-2B] track() reject while bySession already has sessionId must delete inFlight (zero zombie starting:true row).
  let finishCreate10
  fake.handlers['agent.create.request'] = (msg, c) => {
    finishCreate10 = () => {
      fake.upsert(agentSnapshot({ id: msg.agentId, provider: 'my-claude', cwd: '/tmp', currentModeId: 'plan', labels: msg.labels }), PROJECT)
      answerWith(c, 'agent.create.response', { requestId: msg.requestId, agent: null, error: 'late synthetic failure' })
    }
    return null
  }
  const late10 = await plan({ launcher: 'my-claude', session_id: 'plan-zombie-10', prompt: '' })
  assert.equal(late10.session.starting, true)
  finishCreate10()
  await until('zombie-10 listed live and inFlight cleaned', () => bridge.state.bySession.has('plan-zombie-10') && !bridge.state.inFlight.has('plan-zombie-10'))
  fake.archive(fake.requestsOf('agent.create.request').at(-1).agentId)
  await until('archived zombie-10 shows no zombie starting row', () => !rows().some((r) => r.session_id === 'plan-zombie-10' && r.starting))
})
