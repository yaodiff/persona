import assert from 'node:assert/strict'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { POLICY_DIR_NAME, readPolicy, syncPolicy } from '../lib/policy.js'
import { createAgyRuntime } from '../lib/runtime.js'

const RAW = '11111111-2222-4333-8444-555555555555'
const FORK_ID = '22222222-3333-4444-8555-666666666666'
const SESSION = `agy-${RAW}`
const FAKE = fileURLToPath(new URL('./fake-acp-rewind.mjs', import.meta.url))
const SLOW = 15_000
const at = '2026-01-01T00:00:00Z'

const baseRecords = [
  { step_index: 0, type: 'USER_INPUT', content: 'First prompt' },
  { step_index: 1, type: 'PLANNER_RESPONSE', content: 'First answer' },
  { step_index: 2, type: 'USER_INPUT', content: 'Second prompt' },
  { step_index: 3, type: 'PLANNER_RESPONSE', content: 'Second answer' },
].map((step) => ({ ...step, status: 'DONE', created_at: at }))

const nativeRecord = (step) => ({
  type: `CORTEX_STEP_TYPE_${step.type}`,
  metadata: { source: step.type === 'USER_INPUT' ? 'CORTEX_STEP_SOURCE_USER_EXPLICIT' : 'MODEL', createdAt: step.created_at },
  ...(step.type === 'USER_INPUT' ? { userInput: { items: [{ text: step.content }] } } : {}),
  ...(step.type === 'PLANNER_RESPONSE' ? { plannerResponse: { response: step.content } } : {}),
})

const cancelledInputNative = nativeRecord({ step_index: 4, type: 'USER_INPUT', content: 'Interrupt me', status: 'DONE', created_at: at })

async function setupSession(t, {
  extraNativeSteps = [],
  promptPlan = [{ holdForCancel: true, export: true, stopReason: 'cancelled' }],
  summaryStatus = 'CASCADE_RUN_STATUS_RUNNING',
  summarySteps,
  snapshotExtra = {},
  runtimeOpts = {},
  triggerStale = 'cancel', // 'cancel' | 'inactive' | 'none'
} = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-stale-working-'))
  const config = join(home, 'config')
  const logs = join(config, 'brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  writeFileSync(join(logs, 'transcript_full.jsonl'), baseRecords.map((r) => JSON.stringify(r)).join('\n') + '\n')
  writeFileSync(join(home, 'calls.jsonl'), '')

  const nativeSteps = triggerStale === 'none'
    ? [...baseRecords.map(nativeRecord), ...extraNativeSteps]
    : [...baseRecords.map(nativeRecord), cancelledInputNative, ...extraNativeSteps]
  const stepCount = summarySteps ?? nativeSteps.length
  const snapshot = {
    status: 'CASCADE_RUN_STATUS_IDLE',
    forkId: FORK_ID,
    promptPlan,
    trajectory: { cascadeId: RAW, steps: nativeSteps },
    ...snapshotExtra,
  }
  writeFileSync(join(home, 'trajectory.json'), JSON.stringify(snapshot))

  const db = new DatabaseSync(join(config, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  const setSummary = (status, count) => {
    db.prepare('INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)')
      .run(RAW, status, count, 'Stale working example', JSON.stringify([`file://${home}`]), new Date().toISOString())
  }
  setSummary(summaryStatus, stepCount)

  const binary = join(home, 'agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${home}' "$@"\n`)
  chmodSync(binary, 0o700)

  const node = await startFakeNode(t)
  const policyRoot = join(config, POLICY_DIR_NAME)
  const runtime = createAgyRuntime({ home, agyDir: config, agyBin: binary, policyRoot, socketPath: node.path, log: () => {}, ...runtimeOpts })
  t.after(async () => { await runtime.stop(); db.close(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  await until('initial inventory', () => node.last('runtime.sessions')?.sessions.some((s) => s.session_id === SESSION), SLOW)

  const calls = () => readFileSync(join(home, 'calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const row = () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)

  if (triggerStale === 'cancel') {
    await node.peer.call('session.send', { session_id: SESSION, text: 'Interrupt me' })
    await until('held prompt reached fake agy', () => calls().some((c) => c.method === 'session/prompt'), SLOW)
    assert.equal((await node.peer.call('session.interrupt', { session_id: SESSION }))?.settled, true)
    await until('row settles out of working via withoutStaleWorking', () => runtime.idle() && row() && row().status !== 'working', SLOW)
  } else if (triggerStale === 'inactive') {
    // Prompt inactivity marks turn.inactive = true, which records staleWorkingAt while skipping runTurn's
    // post-turn setImmediate(readNativeError), isolating prepareTranscript's own readNativeError path.
    await node.peer.call('session.send', { session_id: SESSION, text: 'Interrupt me' })
    await until('stalled prompt settled via inactivity watchdog', () => runtime.idle() && row() && row().status !== 'working', SLOW)
  }

  return { node, runtime, home, config, policyRoot, db, setSummary, calls, row, stepCount }
}

test('prepareTranscript and readNativeError backfill unexported native errors under stale CASCADE_RUN_STATUS_RUNNING', async (t) => {
  const errorStep = {
    type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE',
    metadata: { createdAt: '2026-01-01T00:00:05Z' },
    errorMessage: { error: { userErrorMessage: 'Native provider error 503', errorCode: 503 } },
  }
  // Use inactivity watchdog so runTurn's setImmediate(readNativeError) is skipped, proving prepareTranscript ->
  // readNativeError itself honors withoutStaleWorking on session.items.
  const { node, calls } = await setupSession(t, {
    extraNativeSteps: [errorStep],
    summarySteps: 6,
    triggerStale: 'inactive',
    runtimeOpts: { promptInactivityMs: 250 },
  })

  assert.equal(
    calls().filter((c) => c.method === 'GetCascadeTrajectorySteps').length,
    0,
    'precondition: runTurn inactivity path must not have pre-fetched native errors',
  )
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.ok(
    calls().some((c) => c.method === 'GetCascadeTrajectorySteps'),
    'prepareTranscript/readNativeError should query GetCascadeTrajectorySteps when running status is stale',
  )
  assert.equal(page.items.at(-1)?.text, 'API Error: Native provider error 503')
})

test('session.modes respects withoutStaleWorking and still blocks on genuine new native steps', async (t) => {
  const { node, calls, setSummary, stepCount } = await setupSession(t)

  await node.peer.call('session.modes', { session_id: SESSION, model: 'test-model' })
  assert.ok(calls().some((c) => c.method === 'session/set_model' && c.params?.modelId === 'test-model'))

  setSummary('CASCADE_RUN_STATUS_RUNNING', stepCount + 1)
  await assert.rejects(
    node.peer.call('session.modes', { session_id: SESSION, model: 'test-model' }),
    /Antigravity session is busy/,
  )
})

test('session.fork respects withoutStaleWorking and still blocks on genuine new native steps', async (t) => {
  const { node, setSummary, stepCount } = await setupSession(t)

  const forked = await node.peer.call('session.fork', { session_id: SESSION })
  assert.equal(forked.session_id, `agy-${FORK_ID}`)

  setSummary('CASCADE_RUN_STATUS_RUNNING', stepCount + 1)
  await assert.rejects(
    node.peer.call('session.fork', { session_id: SESSION }),
    /Wait for the current turn to finish before branching/,
  )
})

test('session.send supervision policy changes respect withoutStaleWorking and still block on genuine new native steps', async (t) => {
  // 1. Stale CASCADE_RUN_STATUS_RUNNING at the recorded mark allows a supervision policy change.
  const accepted = await setupSession(t, {
    promptPlan: [
      { holdForCancel: true, export: true, stopReason: 'cancelled' },
      { reply: 'Policy cleared.', stopReason: 'end_turn' },
    ],
  })
  syncPolicy(accepted.policyRoot, RAW, 'Keep replies brief.')
  assert.equal(readPolicy(accepted.policyRoot, RAW), 'Keep replies brief.')
  await accepted.node.peer.call('session.send', { session_id: SESSION, text: 'Continue', supervision_instructions: '' })
  await until('accepted policy-change turn finishes cleanly', () => accepted.runtime.idle() && readPolicy(accepted.policyRoot, RAW) === '', SLOW)

  // 2. Against a fresh stale-working session (staleWorkingAt populated, runtime idle), advancing step_count
  // past staleWorkingAt represents genuine running native work and must reject policy changes as busy.
  const blocked = await setupSession(t)
  syncPolicy(blocked.policyRoot, RAW, 'Restored policy.')
  assert.equal(blocked.runtime.idle(), true)
  blocked.setSummary('CASCADE_RUN_STATUS_RUNNING', blocked.stepCount + 1)
  await assert.rejects(
    blocked.node.peer.call('session.send', { session_id: SESSION, text: 'Blocked', supervision_instructions: '' }),
    /Wait for the current turn to finish before changing supervision policy/,
  )
})

test('session.rewind succeeds under stale CASCADE_RUN_STATUS_RUNNING, survives async SQLite lag, and re-anchors staleWorkingAt to target.index', async (t) => {
  const { node, home, setSummary, row, stepCount } = await setupSession(t, {
    snapshotExtra: { holdAfterRevertOnce: true },
  })

  const rewinding = node.peer.call('session.rewind', {
    session_id: SESSION,
    prompt: 'Second prompt',
    occurrence: 1,
    prompts_after: 1,
    restore: 'conversation',
  })
  try {
    // Simulate real-world async SQLite lag: RevertToCascadeStep returned, GetCascadeTrajectory is paused, and
    // conversation_summaries.db still holds the pre-rewind step_count (5) while a concurrent reader runs.
    await until('after-revert trajectory held', () => existsSync(join(home, 'after-trajectory-held')), SLOW)
    setSummary('CASCADE_RUN_STATUS_RUNNING', stepCount)
    await node.peer.call('session.items', { session_id: SESSION })
    // Now let SQLite finish syncing step_count down to target.index (2), still with stale RUNNING status.
    setSummary('CASCADE_RUN_STATUS_RUNNING', 2)
  } finally {
    writeFileSync(join(home, 'release-after-trajectory'), '')
  }

  const res = await rewinding
  assert.equal(res.prompt, 'Second prompt')

  // Because staleWorkingAt was re-anchored AFTER SQLite synced to target.index (2), the concurrent reader during
  // async SQLite lag could not delete staleWorkingAt, and the rewound row stays non-working at step_count=2.
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Settled after rewind' })
  await until('rewound row stays non-working at target.index', () => row()?.title === 'Settled after rewind' && row()?.status !== 'working', SLOW)

  // Once new native work advances step_count to 3 (> target.index 2, even though <= pre-rewind mark 5),
  // it must be detected as working and guard modes and rewind again.
  setSummary('CASCADE_RUN_STATUS_RUNNING', 3)
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Refresh after rewind' })
  await until('new native turn at lower step_count is reported working', () => row()?.title === 'Refresh after rewind' && row()?.status === 'working', SLOW)
  await assert.rejects(
    node.peer.call('session.modes', { session_id: SESSION, model: 'test-model' }),
    /Antigravity session is busy/,
  )
  await assert.rejects(
    node.peer.call('session.rewind', {
      session_id: SESSION,
      prompt: 'First prompt',
      occurrence: 1,
      prompts_after: 0,
      restore: 'conversation',
    }),
    /Wait for the current turn to finish before rewinding/,
  )
})

test('session.rewind on a non-stale idle session does not inject staleWorkingAt', async (t) => {
  const { node, setSummary, row } = await setupSession(t, {
    summaryStatus: 'CASCADE_RUN_STATUS_IDLE',
    triggerStale: 'none',
  })

  const res = await node.peer.call('session.rewind', {
    session_id: SESSION,
    prompt: 'Second prompt',
    occurrence: 1,
    prompts_after: 0,
    restore: 'conversation',
  })
  assert.equal(res.prompt, 'Second prompt')

  // Even at step_count === target.index (2), a real RUNNING summary must show working because the session was
  // never stale-working prior to the rewind.
  setSummary('CASCADE_RUN_STATUS_RUNNING', 2)
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Running after clean rewind' })
  await until('non-stale rewind does not mask RUNNING at target.index', () => row()?.title === 'Running after clean rewind' && row()?.status === 'working', SLOW)
})

test('withoutStaleWorking masks stale CANCELING and BUSY summaries after cancel and unmasks when step_count advances', async (t) => {
  for (const staleStatus of ['CASCADE_RUN_STATUS_CANCELING', 'CASCADE_RUN_STATUS_BUSY']) {
    const { node, setSummary, row, stepCount } = await setupSession(t, { summaryStatus: staleStatus })
    assert.notEqual(row().status, 'working', `stale ${staleStatus} must not stay working after cancel`)

    setSummary(staleStatus, stepCount + 1)
    await node.peer.call('session.rename', { session_id: SESSION, title: `Active ${staleStatus}` })
    await until(
      `new native step unmasks ${staleStatus}`,
      () => row()?.title === `Active ${staleStatus}` && row()?.status === 'working',
      SLOW,
    )
  }
})
