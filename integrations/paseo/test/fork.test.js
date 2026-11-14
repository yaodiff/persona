// An agent Paseo moves to another Claude session (a fork: its rewind, and so going on after an API error in Paseo's app)
// stays the same session in Persona (docs/integrations/paseo.md "Session IDs", #6709): the id it was listed under when
// the bridge sees the move, the first session of its transcript's line when it did not.

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createPaseoBridge } from '../lib/bridge.js'
import { projectKey, readHead } from '../lib/lineage.js'
import { PaseoClient } from '../lib/paseo.js'
import { agentSnapshot, startFakePaseo } from './fake-paseo.js'

const A = 'f0f0f0f0-0000-4000-8000-00000000000a'
const B = 'f0f0f0f0-0000-4000-8000-00000000000b'
const C = 'f0f0f0f0-0000-4000-8000-00000000000c'
const D = 'f0f0f0f0-0000-4000-8000-00000000000d'
const PROVIDERS = { claude: { enabled: false }, 'my-claude': { extends: 'claude', label: 'My Claude' }, 'my-codex': { extends: 'codex' } }
const ENTRIES = [
  { provider: 'my-claude', enabled: true, status: 'ready', label: 'My Claude', models: [] },
  { provider: 'my-codex', enabled: true, status: 'ready', label: 'My Codex', models: [] },
]
const iso = (ms) => new Date(ms).toISOString()
const DAY = 86_400_000

/** A temp directory for the test, removed after it. */
function tempDir(t, prefix) {
  const d = mkdtempSync(join('/tmp', prefix))
  t.after(() => rmSync(d, { recursive: true, force: true }))
  return d
}

/**
 * A Claude transcript as Paseo's fork writes it: its records carry the session they were copied from (forkedFrom), the
 * first one the conversation's start.
 */
function transcript(claudeDir, cwd, id, { from, started }) {
  const folder = join(claudeDir, 'projects', projectKey(cwd))
  mkdirSync(folder, { recursive: true })
  const fork = from ? { forkedFrom: { sessionId: from, messageUuid: randomUUID() } } : {}
  const lines = [
    { parentUuid: null, type: 'user', message: { role: 'user', content: 'Please go on' }, uuid: randomUUID(), timestamp: started, sessionId: id, entrypoint: 'sdk-cli', ...fork },
    { type: 'assistant', message: { role: 'assistant', content: [{ type: 'text', text: 'API Error: 499 status code (no body)' }] }, uuid: randomUUID(),
      timestamp: iso(Date.parse(started) + 1000), sessionId: id, isApiErrorMessage: true, ...fork },
  ]
  writeFileSync(join(folder, `${id}.jsonl`), lines.map((l) => JSON.stringify(l)).join('\n') + '\n')
}

/** An agent of Claude Code started in Paseo's own app (no label of Persona's), on session. */
function claudeAgent(session, o = {}) {
  return agentSnapshot({ provider: 'my-claude', title: 'Fixing the UI', status: 'idle', persistence: { provider: 'my-claude', sessionId: session, metadata: { provider: 'claude' } },
    runtimeInfo: { provider: 'my-claude', sessionId: session }, ...o })
}

/** A fake Paseo with agents, a fake node, and the bridge between them, looking for Claude transcripts in claudeHome's .claude. */
async function setup(t, { agents = [], claudeHome = tempDir(t, 'ppfork-home-') } = {}) {
  const fake = await startFakePaseo(t, { providers: PROVIDERS, entries: ENTRIES, agents })
  const node = await startFakeNode(t, { features: ['transcript', 'launchers', 'modes', 'resume', 'unlisted_transcripts'], strict: true })
  const paseoHome = tempDir(t, 'ppfork-')
  writeFileSync(join(paseoHome, 'paseo.pid'), JSON.stringify({ pid: process.pid, listen: `127.0.0.1:${fake.port}` }))
  const logs = []
  const paseo = new PaseoClient({ home: paseoHome, retryMin: 30, retryMax: 100, log: (m) => logs.push(m) })
  const bridge = createPaseoBridge({ paseo, log: (m) => logs.push(m) }, { socketPath: node.path, retryMs: 30, pushDelayMs: 5, refreshMs: 0, awayGrace: 400,
    callWait: 1500, sweepMs: 0, claudeHome, claudeEnv: {}, allowTools: ['mcp__persona__report_to_supervisor'] })
  bridge.start()
  t.after(() => bridge.stop())
  await node.ready
  const pushed = () => node.last('runtime.sessions') ?? { sessions: [] }
  await until('the first list', () => node.last('runtime.sessions'))
  return { fake, node, bridge, logs, pushed, rows: () => pushed().sessions }
}

const setupWith = (t, claudeHome, o) => setup(t, { ...o, claudeHome })

test("a fork the bridge sees: the agent keeps the id it was listed under, pinned on Paseo's record as kept, its transcript the fork's", async (t) => {
  const agent = claudeAgent(A)
  const { fake, node, rows, pushed } = await setup(t, { agents: [{ agent }] })
  await until('listed under its Claude session', () => rows().some((r) => r.session_id === A))
  // Paseo goes on after an API error: it forks the session first, and the agent runs on the fork.
  fake.update(agent.id, { status: 'running', persistence: { provider: 'my-claude', sessionId: B, metadata: { provider: 'claude' } }, runtimeInfo: { provider: 'my-claude', sessionId: B } })
  await until('the fork its transcript', () => rows().find((r) => r.session_id === A)?.transcript === B)
  assert.equal(rows().some((r) => r.session_id === B), false, "no row under the fork's id")
  assert.equal(node.all('runtime.sessions').some((p) => p.sessions.some((r) => r.session_id === B)), false, 'in no list at all')
  await until('pinned', () => fake.requestsOf('update_agent_request').length === 1)
  assert.deepEqual(fake.requestsOf('update_agent_request')[0], { ...fake.requestsOf('update_agent_request')[0], agentId: agent.id,
    labels: { 'persona.session': A, 'persona.kept': '1' } })
  await until('the label on the record', () => fake.agents.get(agent.id).agent.labels['persona.session'] === A)
  // Forked again (a second error): the label holds it, nothing more to pin.
  fake.update(agent.id, { status: 'idle', persistence: { provider: 'my-claude', sessionId: C, metadata: { provider: 'claude' } }, runtimeInfo: { provider: 'my-claude', sessionId: C } })
  await until('the next fork', () => rows().find((r) => r.session_id === A)?.transcript === C)
  assert.equal(rows().filter((r) => r.title === 'Fixing the UI').length, 1, 'one row for the conversation')
  assert.equal(fake.requestsOf('update_agent_request').length, 1, 'pinned once')
  assert.deepEqual(pushed().unlisted_transcripts ?? [], [], 'none of its earlier transcripts named')
  // Its requests are the same session's, and a kept label is not an Agent Persona started: Persona's own tools still ask.
  fake.permission(agent.id, { id: 'perm-1', provider: 'claude', name: 'mcp__persona__report_to_supervisor', kind: 'tool', input: { kind: 'done', text: 'x' } })
  await until('the card', () => node.asked.length === 1)
  assert.deepEqual([node.asked[0].session_id, node.asked[0].tool_name], [A, 'mcp__persona__report_to_supervisor'])
  assert.equal(fake.requestsOf('agent_permission_response').length, 0, 'not allowed by the bridge')
})

test("an agent Persona started keeps its label's id through a fork: nothing is pinned", async (t) => {
  const agent = claudeAgent(A, { labels: { 'persona.session': 'paseo-own' } })
  const { fake, rows } = await setup(t, { agents: [{ agent }] })
  await until('listed', () => rows().some((r) => r.session_id === 'paseo-own'))
  fake.update(agent.id, { persistence: { provider: 'my-claude', sessionId: B, metadata: { provider: 'claude' } } })
  await until('the fork its transcript', () => rows().find((r) => r.session_id === 'paseo-own')?.transcript === B)
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(fake.requestsOf('update_agent_request').length, 0)
})

test('a fork the bridge did not see: the id is the first session of the line, for an agent that began its conversation itself', async (t) => {
  const claudeHome = tempDir(t, 'ppfork-claude-')
  const claudeDir = join(claudeHome, '.claude')
  const cwd = tempDir(t, 'ppfork-work-')
  const created = Date.now() - DAY
  // Its conversation began a second after the agent was made; Paseo forked it twice while the bridge was away.
  transcript(claudeDir, cwd, A, { started: iso(created + 1000) })
  transcript(claudeDir, cwd, B, { from: A, started: iso(created + 1000) })
  transcript(claudeDir, cwd, C, { from: B, started: iso(created + 1000) })
  const agent = claudeAgent(C, { cwd, createdAt: iso(created), updatedAt: iso(created + 5000) })
  const { fake, rows, pushed } = await setupWith(t, claudeHome, { agents: [{ agent }] })
  await until('listed', () => rows().some((r) => r.title === 'Fixing the UI'))
  const row = rows().find((r) => r.title === 'Fixing the UI')
  assert.deepEqual([row.session_id, row.transcript], [A, C], 'under the line\'s first session, its content the latest fork')
  assert.equal(rows().length, 1)
  assert.equal(fake.requestsOf('update_agent_request').length, 0, 'read at connect: nothing written to Paseo')
  await until('the archive read', () => pushed().unlisted_transcripts)
  assert.deepEqual(pushed().unlisted_transcripts, [])
  // Its next change in Paseo pins it.
  fake.update(agent.id, { status: 'running' })
  await until('pinned', () => fake.requestsOf('update_agent_request').length === 1)
  assert.deepEqual(fake.requestsOf('update_agent_request')[0].labels, { 'persona.session': A, 'persona.kept': '1' })
  assert.equal(rows().find((r) => r.title === 'Fixing the UI').session_id, A)
})

test('a line is not followed for a conversation that began before its agent, nor into another agent\'s session; a gone first transcript still names the line', async (t) => {
  const claudeHome = tempDir(t, 'ppfork-claude-')
  const claudeDir = join(claudeHome, '.claude')
  const cwd = tempDir(t, 'ppfork-work-')
  const created = Date.now() - DAY
  // 1. Handed over from another agent (or imported): its conversation began a day before it was made.
  transcript(claudeDir, cwd, A, { started: iso(created - DAY) })
  transcript(claudeDir, cwd, B, { from: A, started: iso(created - DAY) })
  const handed = claudeAgent(B, { cwd, title: 'handed over', createdAt: iso(created), updatedAt: iso(created) })
  // 2. A fork of a session another agent still has: that agent's branch, not its line.
  const OTHER = 'f0f0f0f0-0000-4000-8000-0000000000e1'
  const BRANCH = 'f0f0f0f0-0000-4000-8000-0000000000e2'
  transcript(claudeDir, cwd, OTHER, { started: iso(created + 1000) })
  transcript(claudeDir, cwd, BRANCH, { from: OTHER, started: iso(created + 1000) })
  const owner = claudeAgent(OTHER, { cwd, title: 'the original', createdAt: iso(created), updatedAt: iso(created) })
  const branch = claudeAgent(BRANCH, { cwd, title: 'a branch', createdAt: iso(created), updatedAt: iso(created) })
  // 3. The line's first transcript is gone: its id is still the line's, from the fork's records.
  transcript(claudeDir, cwd, D, { from: C, started: iso(created + 1000) })
  const pruned = claudeAgent(D, { cwd, title: 'pruned', createdAt: iso(created), updatedAt: iso(created) })
  const { rows } = await setupWith(t, claudeHome, { agents: [handed, owner, branch, pruned].map((agent) => ({ agent })) })
  await until('all listed', () => rows().length === 4)
  const id = (title) => rows().find((r) => r.title === title)?.session_id
  assert.deepEqual([id('handed over'), id('the original'), id('a branch'), id('pruned')], [B, OTHER, BRANCH, C])
})

test('an id kept for an agent gives way to an agent whose own session it becomes (Paseo imported the earlier transcript)', async (t) => {
  const claudeHome = tempDir(t, 'ppfork-claude-')
  const claudeDir = join(claudeHome, '.claude')
  const cwd = tempDir(t, 'ppfork-work-')
  const created = Date.now() - DAY
  transcript(claudeDir, cwd, A, { started: iso(created + 1000) })
  transcript(claudeDir, cwd, B, { from: A, started: iso(created + 1000) })
  const agent = claudeAgent(B, { cwd, createdAt: iso(created), updatedAt: iso(created + 5000) })
  const { fake, rows } = await setupWith(t, claudeHome, { agents: [{ agent }] })
  await until('kept', () => rows().some((r) => r.session_id === A && r.title === 'Fixing the UI'))
  fake.upsert(claudeAgent(A, { cwd, title: 'imported', createdAt: iso(Date.now()), updatedAt: iso(Date.now()) }))
  await until('the session is the importing agent\'s', () => rows().find((r) => r.session_id === A)?.title === 'imported')
  fake.update(agent.id, { status: 'running' })
  await until('the first agent under its own session again', () => rows().find((r) => r.session_id === B)?.title === 'Fixing the UI')
  assert.equal(fake.requestsOf('update_agent_request').length, 0, 'nothing pinned for an id another agent has')
})

test("a branch Persona made of a forked conversation is a session of its own, never the original's line", async (t) => {
  const claudeHome = tempDir(t, 'ppfork-claude-')
  const claudeDir = join(claudeHome, '.claude')
  const cwd = tempDir(t, 'ppfork-work-')
  const created = Date.now() - DAY
  // The original: begun in Paseo's app, forked once (a rewind), kept under its line's first session.
  transcript(claudeDir, cwd, A, { started: iso(created + 1000) })
  transcript(claudeDir, cwd, B, { from: A, started: iso(created + 1000) })
  const original = claudeAgent(B, { cwd, createdAt: iso(created), updatedAt: iso(created + 5000) })
  // Branch to new chat copies B's records under a new session as they are: its first record still names A, B's parent.
  transcript(claudeDir, cwd, C, { from: A, started: iso(created + 1000) })
  transcript(claudeDir, cwd, D, { from: A, started: iso(created + 1000) })
  const { node, fake, rows } = await setupWith(t, claudeHome, { agents: [{ agent: original }] })
  await until('the original kept', () => rows().some((r) => r.session_id === A))
  // Its first message has Paseo take it over (session.start with resume): imported under its own id, on the label.
  const res = await node.peer.call('session.start', { cwd, resume: C, launcher: 'my-claude' })
  assert.equal(res.session.session_id, C)
  assert.deepEqual(fake.requestsOf('import_agent_request').map((r) => r.labels), [{ 'persona.session': C }])
  // A copy Paseo's own app imported, without a label, made seconds after the conversation began: A is the original's.
  fake.upsert(claudeAgent(D, { cwd, title: 'copy', createdAt: iso(created + 2000), updatedAt: iso(Date.now()) }))
  await until('both listed', () => rows().some((r) => r.session_id === C) && rows().some((r) => r.session_id === D))
  assert.equal(rows().find((r) => r.session_id === A)?.transcript, B, 'the original is still its own line')
  assert.equal(rows().filter((r) => r.session_id === A).length, 1)
  assert.equal(fake.requestsOf('update_agent_request').length, 0)
})

test("an archived agent Paseo forked before the bridge started: a row of its own under the line's first id, no transcript named", async (t) => {
  const claudeHome = tempDir(t, 'ppfork-claude-')
  const claudeDir = join(claudeHome, '.claude')
  const cwd = tempDir(t, 'ppfork-work-')
  const created = Date.now() - 2 * DAY
  transcript(claudeDir, cwd, A, { started: iso(created + 1000) })
  transcript(claudeDir, cwd, B, { from: A, started: iso(created + 1000) })
  const agent = claudeAgent(B, { cwd, status: 'closed', createdAt: iso(created), updatedAt: iso(Date.now() - DAY), archivedAt: iso(Date.now() - DAY) })
  const { node, fake, rows, pushed } = await setupWith(t, claudeHome, { agents: [{ agent }] })
  await until('the archive read', () => pushed().unlisted_transcripts)
  await until('its row', () => rows().some((r) => r.session_id === A))
  const row = rows().find((r) => r.session_id === A)
  assert.deepEqual([row.status, row.transcript], ['offline', B])
  assert.deepEqual(pushed().unlisted_transcripts, [], 'neither transcript is a session not running of the node\'s')
  // A message takes it back from the archive under the same id.
  await node.peer.call('session.send', { session_id: A, text: 'go on', client_msg_id: 'cm-1' })
  assert.equal(fake.requestsOf('send_agent_message_request').at(-1).agentId, agent.id)
  await until('back, and pinned', () => fake.requestsOf('update_agent_request').some((r) => r.labels['persona.session'] === A))
  await until('running under the same id', () => rows().find((r) => r.session_id === A)?.status === 'working')
  assert.equal(rows().filter((r) => r.title === 'Fixing the UI').length, 1)
})

test('a Codex agent is its agent id whatever its thread: nothing kept', async (t) => {
  const agent = agentSnapshot({ provider: 'my-codex', title: 'codex', persistence: { provider: 'my-codex', sessionId: A, metadata: { provider: 'codex', threadId: A } } })
  const { fake, rows } = await setup(t, { agents: [{ agent }] })
  await until('listed', () => rows().some((r) => r.session_id === `paseo-${agent.id}`))
  fake.update(agent.id, { persistence: { provider: 'my-codex', sessionId: B, metadata: { provider: 'codex', threadId: B } } })
  await until('the new thread', () => rows().find((r) => r.session_id === `paseo-${agent.id}`)?.codex_thread === B)
  assert.equal(fake.requestsOf('update_agent_request').length, 0)
})

test("a transcript's head: the session it was forked from and when its conversation began; a first line too long is not read", (t) => {
  const dir = tempDir(t, 'ppfork-head-')
  transcript(dir, '/w', B, { from: A, started: '2026-11-01T00:00:01.000Z' })
  const path = join(dir, 'projects', projectKey('/w'), `${B}.jsonl`)
  assert.deepEqual(readHead(path, B), { parent: A, startedAt: Date.parse('2026-11-01T00:00:01.000Z') })
  transcript(dir, '/w', A, { started: '2026-11-01T00:00:01.000Z' })
  assert.deepEqual(readHead(join(dir, 'projects', projectKey('/w'), `${A}.jsonl`), A), { parent: undefined, startedAt: Date.parse('2026-11-01T00:00:01.000Z') })
  const big = join(dir, 'big.jsonl')
  writeFileSync(big, JSON.stringify({ type: 'user', message: { content: 'x'.repeat(9 << 20) }, forkedFrom: { sessionId: A } }) + '\n')
  assert.equal(readHead(big, B), undefined)
  assert.equal(readHead(join(dir, 'missing.jsonl'), B), undefined)
})
