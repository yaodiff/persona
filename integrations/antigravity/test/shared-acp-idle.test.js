import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { toAgyId } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const policyFake = fileURLToPath(new URL('./fake-acp-policy.mjs', import.meta.url))
const modelsFake = fileURLToPath(new URL('./fake-acp-models.mjs', import.meta.url))

async function policyFixture(t, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-shared-idle-'))
  const binary = join(home, 'agy')
  const record = join(home, 'requests.jsonl')
  const logs = []
  writeFileSync(record, '')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${policyFake}' --record '${record}' "$@"\n`, { mode: 0o700 })
  const node = await startFakeNode(t, { features: ['assistant_pids'] })
  const runtime = createAgyRuntime({
    home, agyBin: binary, personadBin: binary, socketPath: node.path,
    log: (line) => logs.push(line), ...options,
  })
  t.after(async () => { await runtime.stop(); rmSync(home, { recursive: true, force: true }) })
  await runtime.start()
  await node.ready
  const calls = () => readFileSync(record, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
  const startedPids = () => calls().filter((entry) => entry.started).map((entry) => entry.started)
  const start = async (params = {}) => (await node.peer.call('session.start', { cwd: home, ...params })).session.session_id
  const call = (method, session_id, params = {}) => node.peer.call(`session.${method}`, { session_id, ...params })
  const request = (method, session) => calls().findLast((entry) => entry.method === method && entry.params?.sessionId === toAgyId(session))
  return { node, runtime, logs, calls, startedPids, start, call, request }
}

test('shared Agent worker retires quietly after idle timeout, spares active turns and permissions, and resumes on demand', async (t) => {
  const { node, logs, startedPids, start, call, request } = await policyFixture(t, { sharedAcpIdleMs: 250, promptInactivityMs: 0 })
  const pending = Promise.withResolvers()
  node.resolve = () => pending.promise

  const busy = await start({ prompt: 'hold' })
  const waiting = await start()
  await call('modes', waiting, { effort: 'ask-idle' })
  await until('permission requested outside a prompt', () => node.asked.some((card) => card.session_id === waiting))
  assert.equal(startedPids().length, 1)
  const firstPid = startedPids()[0]

  await delay(450)
  process.kill(firstPid, 0)

  assert.equal((await call('interrupt', busy)).settled, true)
  await delay(400)
  process.kill(firstPid, 0)

  pending.resolve({ allow: true })
  await until('idle shared worker exits', () => {
    try { process.kill(firstPid, 0); return false } catch (error) { assert.equal(error.code, 'ESRCH'); return true }
  })
  assert.deepEqual(logs.filter((line) => /prompt error|replacing/.test(line)), [])

  await call('send', waiting, { text: 'after-idle' })
  const resumed = await until('prompt runs on a fresh shared worker', () => request('session/prompt', waiting))
  assert.notEqual(resumed.worker, firstPid)
  assert.equal(request('session/resume', waiting)?.worker, resumed.worker)
  assert.deepEqual(startedPids(), [firstPid, resumed.worker])
})

test('an Agent send during shared worker idle retirement waits for the retiring process to exit first', async (t) => {
  const { calls, startedPids, start, call, request } = await policyFixture(t, {
    agyArgs: ['--ignore-term'], sharedAcpIdleMs: 250,
  })
  const session = await start()
  const firstPid = await until('shared worker started', () => startedPids()[0])
  await until('shared worker entered idle retirement', () => calls().some((entry) => entry.term === firstPid))
  process.kill(firstPid, 0)
  await call('send', session, { text: 'resume-after-retire' })
  assert.throws(() => process.kill(firstPid, 0), { code: 'ESRCH' })
  const resumed = await until('replacement shared worker receives prompt', () => request('session/prompt', session))
  assert.notEqual(resumed.worker, firstPid)
  assert.deepEqual(startedPids(), [firstPid, resumed.worker])
  process.kill(resumed.worker, 'SIGKILL')
})

test('session context_window persists across shared worker idle retirement', async (t) => {
  const entries = ['medium', 'high', 'low'].map((level) => ({ modelId: `gemini-future-${level}`, name: `Gemini Future (${level})` }))
  const home = mkdtempSync(join(tmpdir(), 'agy-shared-window-'))
  const agyDir = join(home, 'settings')
  mkdirSync(agyDir, { recursive: true })
  const catalogFile = join(home, 'catalog.json')
  const writeCatalog = (value) => {
    writeFileSync(`${catalogFile}.next`, JSON.stringify(value))
    renameSync(`${catalogFile}.next`, catalogFile)
  }
  writeCatalog({ models: entries, windows: { [entries[1].modelId]: 524288, [entries[2].modelId]: 131072 } })
  writeFileSync(join(agyDir, 'settings.json'), JSON.stringify({ model: entries[1].name, agentMode: 'default' }))
  const bin = join(home, 'agy')
  writeFileSync(bin, `#!/bin/sh\nexec '${process.execPath}' '${modelsFake}' '${home}' "$@"\n`)
  chmodSync(bin, 0o755)
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ home, agyDir, agyBin: bin, personadBin: '/missing', socketPath: node.path, sharedAcpIdleMs: 250, log: () => {} })
  t.after(async () => { await runtime.stop(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await node.ready

  const { session } = await node.peer.call('session.start', { cwd: home })
  const row = () => node.last('runtime.sessions')?.sessions.find((entry) => entry.session_id === session.session_id)
  const calls = () => readFileSync(join(home, 'calls.jsonl'), 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
  await until('initial ACP window published', () => row()?.context_window === 524288)
  assert.equal(calls().filter((entry) => entry.method === 'initialize').length, 1)

  await delay(450)
  await node.peer.call('session.rename', { session_id: session.session_id, title: 'Still idle' })
  await until('renamed row published while shared worker is retired', () => row()?.title === 'Still idle')
  assert.equal(row()?.context_window, 524288)
  assert.equal(calls().filter((entry) => entry.method === 'initialize').length, 1)

  await node.peer.call('session.modes', { session_id: session.session_id, model: entries[2].modelId })
  await until('resumed worker reports updated model window', () => row()?.context_window === 131072)
  assert.equal(calls().filter((entry) => entry.method === 'initialize').length, 2)

  await delay(450)
  await node.peer.call('session.send', { session_id: session.session_id, text: 'Continue after retirement' })
  await until('prompt runs after the second worker is retired', () => calls().some((entry) => entry.method === 'session/prompt'))
  const requests = calls().slice(calls().findLastIndex((entry) => entry.method === 'initialize'))
  assert.equal(calls().filter((entry) => entry.method === 'initialize').length, 3)
  const setModel = requests.findIndex((entry) => entry.method === 'session/set_model' && entry.params.modelId === entries[2].modelId)
  assert.ok(setModel > requests.findIndex((entry) => entry.method === 'session/resume'))
  assert.ok(setModel < requests.findIndex((entry) => entry.method === 'session/prompt'))
})
