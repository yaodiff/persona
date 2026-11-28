import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE = fileURLToPath(new URL('./fake-acp-usage.mjs', import.meta.url))

async function fixture(t, features, extraOptions = { usageWindowsEveryMs: 20 }) {
  const home = mkdtempSync(join(tmpdir(), 'agy-usage-'))
  const agyDir = join(home, 'agy')
  mkdirSync(agyDir, { recursive: true })
  const quotaFile = join(home, 'quota.json')
  writeFileSync(quotaFile, JSON.stringify({ buckets: [
    { bucketId: '3p-5h', window: '18000s', remainingFraction: 0.75, resetTime: '2099-01-01T00:00:00Z' },
  ] }))
  writeFileSync(join(home, 'native-calls.jsonl'), '')
  writeFileSync(join(home, 'acp-starts.jsonl'), '')
  const binary = join(home, 'fake-agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${home}' "$@"\n`)
  chmodSync(binary, 0o700)
  const node = await startFakeNode(t, { features })
  const logs = []
  const runtime = createAgyRuntime({ home, agyDir, agyBin: binary, personadBin: '/missing', socketPath: node.path, log: (line) => logs.push(line), ...extraOptions })
  t.after(async () => { await runtime.stop(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  const calls = () => readFileSync(join(home, 'native-calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const starts = () => readFileSync(join(home, 'acp-starts.jsonl'), 'utf8').split('\n').filter(Boolean).map((line) => JSON.parse(line).pid)
  return { home, node, runtime, quotaFile, calls, starts, logs }
}

const NATIVE_ID = 'a12624d1-e812-408f-8f85-d52a2e0844a1'

function writeNativeTranscript(home, updatedAt = new Date()) {
  const logs = join(home, 'agy', 'brain', NATIVE_ID, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  const transcript = join(logs, 'transcript_full.jsonl')
  writeFileSync(transcript, JSON.stringify({ step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', content: 'Terminal prompt' }) + '\n')
  utimesSync(transcript, updatedAt, updatedAt)
}

function refreshOnHubReconnect(node) {
  const lists = node.all('runtime.sessions').length
  node.setHub(false)
  node.setHub(true)
  return until('session list after reconnect', () => node.all('runtime.sessions')[lists])
}

async function waitForWorkerExit(pid) {
  assert.ok(pid)
  await until('idle ACP worker exits', () => {
    try { process.kill(pid, 0); return false } catch (error) { assert.equal(error.code, 'ESRCH'); return true }
  })
}

test('agy polls native quota summaries and publishes normalized usage windows', async (t) => {
  const { node, runtime, quotaFile, calls } = await fixture(t, ['usage_windows'])
  const params = await until('first usage window report', () => node.last('usage.windows'))
  assert.equal(params.provider, 'agy')
  assert.equal(params.name, 'Antigravity')
  assert.ok(Number.isFinite(Date.parse(params.sampled_at)))
  assert.deepEqual(params.windows, [{ label: '5h', window_minutes: 300, used_percent: 25, resets_at: '2099-01-01T00:00:00.000Z' }])
  assert.equal(calls()[0].method, 'RetrieveUserQuotaSummary')
  assert.deepEqual(calls()[0].params, { request: { forceRefresh: false } })

  writeFileSync(quotaFile, JSON.stringify({ buckets: [] }))
  await until('empty native snapshot clears the account', () => node.last('usage.windows')?.windows?.length === 0)
  const count = calls().length
  await runtime.stop()
  await new Promise((resolve) => setTimeout(resolve, 60))
  assert.equal(calls().length, count, 'a stopped runtime does not poll again')
})

test('agy does not start a native quota reader against an older node', async (t) => {
  const { node, calls } = await fixture(t, [])
  await new Promise((resolve) => setTimeout(resolve, 80))
  assert.equal(node.last('usage.windows'), undefined)
  assert.deepEqual(calls(), [])
})

test('recent terminal activity samples quota on demand and the quota-only worker retires', async (t) => {
  const { home, node, quotaFile, calls, starts } = await fixture(t, ['usage_windows'], { sharedAcpIdleMs: 250 })
  await until('initial empty session list', () => node.last('runtime.sessions'))
  writeFileSync(join(home, 'user-status.json'), JSON.stringify({
    userStatus: { email: 'me@example.com', userTier: { name: 'Google AI Pro' } },
  }))
  writeNativeTranscript(home)

  const list = await refreshOnHubReconnect(node)
  assert.equal(list.sessions[0].session_id, `agy-${NATIVE_ID}`)
  const report = await until('quota from terminal activity', () => node.last('usage.windows'))
  assert.equal(report.windows[0].used_percent, 25)
  assert.equal(report.name, 'me@example.com')
  assert.equal(report.plan, 'Google AI Pro')
  const [pid] = starts()
  assert.deepEqual(calls(), [
    { method: 'RetrieveUserQuotaSummary', params: { request: { forceRefresh: false } }, worker: pid },
    { method: 'GetUserStatus', params: { metadata: {} }, worker: pid },
  ])
  await waitForWorkerExit(pid)

  const reports = node.all('usage.windows').length
  await refreshOnHubReconnect(node)
  assert.deepEqual(node.all('usage.windows').slice(reports), [report], 'the reconnect replays the fresh sample')
  assert.deepEqual(starts(), [pid])

  const now = Date.now.bind(Date)
  t.mock.method(Date, 'now', () => now() + 6 * 60_000)
  writeFileSync(quotaFile, JSON.stringify({ buckets: [] }))
  await refreshOnHubReconnect(node)
  await until('stale quota is sampled again', () => node.last('usage.windows')?.windows?.length === 0)
  assert.equal(starts().length, 2)
  await waitForWorkerExit(starts()[1])
})

test('an idle runtime with old terminal history starts no ACP worker for quota', async (t) => {
  const { home, node, runtime, calls, starts } = await fixture(t, ['usage_windows'], {})
  await until('initial empty session list', () => node.last('runtime.sessions'))
  writeNativeTranscript(home, new Date(Date.now() - 60 * 60_000))

  const list = await refreshOnHubReconnect(node)
  assert.equal(list.sessions[0].session_id, `agy-${NATIVE_ID}`)
  assert.equal(runtime.idle(), true)
  assert.deepEqual(starts(), [])
  assert.deepEqual(calls(), [])
  assert.equal(node.last('usage.windows'), undefined)
})

test('terminal activity backs off after a failed quota read', async (t) => {
  const { home, node, runtime, quotaFile, logs, calls, starts } = await fixture(t, ['usage_windows'], { sharedAcpIdleMs: 250 })
  await until('initial empty session list', () => node.last('runtime.sessions'))
  writeFileSync(quotaFile, JSON.stringify({ buckets: true }))
  const db = new DatabaseSync(join(home, 'agy', 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  db.prepare('INSERT INTO conversation_summaries (conversation_id, status) VALUES (?, ?)').run(NATIVE_ID, 'CASCADE_RUN_STATUS_RUNNING')
  db.close()

  const list = await refreshOnHubReconnect(node)
  assert.equal(list.sessions[0].status, 'working')
  await until('quota read failed', () => logs.some((line) => line.includes('quota bucket collections must be arrays')))
  const [pid] = starts()
  await waitForWorkerExit(pid)
  await refreshOnHubReconnect(node)
  assert.equal(runtime.idle(), true)
  assert.deepEqual(starts(), [pid])
  assert.deepEqual(calls().map(({ method }) => method), ['RetrieveUserQuotaSummary', 'GetUserStatus'])
  assert.equal(node.last('usage.windows'), undefined)
})

test('default runtime never spawns an idle ACP process for quota and samples from live workers without keeping them awake', async (t) => {
  const { home, node, quotaFile, calls, starts } = await fixture(t, ['usage_windows', 'assistant_pids'], {
    sharedAcpIdleMs: 250, assistantAcpIdleMs: 250,
  })
  await new Promise((resolve) => setTimeout(resolve, 120))
  assert.deepEqual(starts(), [], 'idle startup spawns no agy acp process')
  assert.deepEqual(calls(), [])
  assert.equal(node.last('usage.windows'), undefined)

  const { session } = await node.peer.call('session.start', { cwd: home })
  await until('session open samples quota from shared worker', () => node.last('usage.windows')?.windows?.[0]?.used_percent === 25)
  assert.equal(starts().length, 1)
  const sharedPid = starts()[0]
  assert.equal(calls().at(-1)?.worker, sharedPid)

  writeFileSync(quotaFile, JSON.stringify({ buckets: [
    { bucketId: '3p-5h', window: '18000s', remainingFraction: 0.5, resetTime: '2099-01-01T00:00:00Z' },
  ] }))
  await node.peer.call('session.send', { session_id: session.session_id, text: 'run turn' })
  await until('turn completion refreshes quota from shared worker', () => node.last('usage.windows')?.windows?.[0]?.used_percent === 50)

  await until('shared worker still retires after idle window', () => {
    try { process.kill(sharedPid, 0); return false } catch (error) { assert.equal(error.code, 'ESRCH'); return true }
  })

  writeFileSync(quotaFile, JSON.stringify({ buckets: [
    { bucketId: '3p-5h', window: '18000s', remainingFraction: 0.2, resetTime: '2099-01-01T00:00:00Z' },
  ] }))
  await node.peer.call('session.start', { cwd: home, assistant: true })
  await until('assistant session samples quota from its own worker without spawning shared worker', () => node.last('usage.windows')?.windows?.[0]?.used_percent === 80)
  assert.equal(starts().length, 2)
  const assistantPid = starts()[1]
  assert.equal(calls().at(-1)?.worker, assistantPid)
  await until('assistant worker also retires after idle window', () => {
    try { process.kill(assistantPid, 0); return false } catch (error) { assert.equal(error.code, 'ESRCH'); return true }
  })
})

test('agy enriches usage windows with signed-in account name and plan from GetUserStatus and falls back on errors', async (t) => {
  const { home, node, quotaFile, calls, starts } = await fixture(t, ['usage_windows', 'assistant_pids'], {
    sharedAcpIdleMs: 250, assistantAcpIdleMs: 250,
  })
  const statusFile = join(home, 'user-status.json')
  writeFileSync(statusFile, JSON.stringify({
    userStatus: {
      name: 'Alice Example',
      email: 'alice@example.com',
      planStatus: { planInfo: { teamsTier: 'TEAMS_TIER_PRO', planName: 'Pro' } },
      userTier: { id: 'g1-ultra-tier', name: 'Google AI Ultra' },
    },
  }))

  const { session } = await node.peer.call('session.start', { cwd: home })
  const first = await until('session open publishes account name and plan', () => {
    const report = node.last('usage.windows')
    return report?.name === 'alice@example.com' ? report : undefined
  })
  assert.equal(first.provider, 'agy')
  assert.equal(first.plan, 'Google AI Ultra')
  assert.equal(first.windows[0].used_percent, 25)
  assert.equal(starts().length, 1)
  const sharedPid = starts()[0]
  const statusCall = calls().find((c) => c.method === 'GetUserStatus')
  assert.ok(statusCall, 'expected GetUserStatus call on live worker')
  assert.deepEqual(statusCall.params, { metadata: {} })
  assert.equal(statusCall.worker, sharedPid)

  writeFileSync(quotaFile, JSON.stringify({ buckets: [
    { bucketId: '3p-5h', window: '18000s', remainingFraction: 0.4, resetTime: '2099-01-01T00:00:00Z' },
  ] }))
  writeFileSync(statusFile, JSON.stringify({ httpStatus: 500, message: 'status temporarily unavailable' }))
  await node.peer.call('session.send', { session_id: session.session_id, text: 'fallback turn' })
  const degraded = await until('GetUserStatus failure still publishes quota with Antigravity fallback', () => {
    const report = node.last('usage.windows')
    return report?.windows?.[0]?.used_percent === 60 ? report : undefined
  })
  assert.equal(degraded.name, 'Antigravity')
  assert.equal('plan' in degraded, false)

  writeFileSync(quotaFile, JSON.stringify({ buckets: [
    { bucketId: '3p-5h', window: '18000s', remainingFraction: 0.3, resetTime: '2099-01-01T00:00:00Z' },
  ] }))
  writeFileSync(statusFile, JSON.stringify({
    userStatus: {
      email: `${'b'.repeat(140)}@example.com`,
      userTier: { name: 'Google AI Pro' },
    },
  }))
  await node.peer.call('session.send', { session_id: session.session_id, text: 'switched account turn' })
  const switched = await until('updated GetUserStatus passes runtime-sdk 128-byte validation', () => {
    const report = node.last('usage.windows')
    return report?.windows?.[0]?.used_percent === 70 ? report : undefined
  })
  assert.equal(switched.name, 'b'.repeat(128))
  assert.equal(switched.plan, 'Google AI Pro')

  await until('worker still retires after idle window with GetUserStatus enabled', () => {
    try { process.kill(sharedPid, 0); return false } catch (error) { assert.equal(error.code, 'ESRCH'); return true }
  })
})
