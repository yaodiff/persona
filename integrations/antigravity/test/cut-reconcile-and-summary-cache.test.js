import assert from 'node:assert/strict'
import { appendFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { rememberObservedWake, readTranscript, stepsToItems } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const RAW = '33333333-4444-4555-8666-777777777777'
const SESSION = `agy-${RAW}`
const AT = '2026-01-01T00:00:00Z'

const baseRecords = [
  { step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', content: 'First question' },
  { step_index: 1, type: 'PLANNER_RESPONSE', content: 'First answer', model: 'gemini-4-carbon-high', input_tokens: 1000, output_tokens: 50 },
  { step_index: 2, type: 'USER_INPUT', source: 'USER_EXPLICIT', content: 'Second question' },
  { step_index: 3, type: 'PLANNER_RESPONSE', content: 'Second answer', model: 'gemini-4-carbon-high', input_tokens: 2000, output_tokens: 80 },
].map((step) => ({ ...step, status: 'DONE', created_at: AT }))

const toNative = (step) => ({
  type: `CORTEX_STEP_TYPE_${step.type}`,
  metadata: {
    source: step.type === 'USER_INPUT' ? 'CORTEX_STEP_SOURCE_USER_EXPLICIT' : 'MODEL',
    createdAt: step.created_at,
  },
  ...(step.type === 'USER_INPUT' ? { userInput: { items: [{ text: step.content }] } } : {}),
  ...(step.type === 'PLANNER_RESPONSE' ? { plannerResponse: { response: step.content } } : {}),
})

const FAKE_ACP_SOURCE = `
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { createInterface } from 'node:readline'

if (process.argv.includes('models')) { console.log('test-model\\tTest Model'); process.exit(0) }

const dir = process.argv[2]
const logFile = process.argv[process.argv.indexOf('--log-file') + 1]
const callsFile = dir + '/calls.jsonl'
const send = (msg) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...msg }) + '\\n')

const server = createServer(async (req, res) => {
  const chunks = []
  for await (const chunk of req) chunks.push(chunk)
  const params = JSON.parse(Buffer.concat(chunks).toString())
  const method = req.url.split('/').at(-1)
  appendFileSync(callsFile, JSON.stringify({ worker: process.pid, method, params }) + '\\n')
  const state = JSON.parse(readFileSync(dir + '/trajectory.json', 'utf8'))
  if (method === 'GetCascadeTrajectory') {
    if (state.failTrajectory) {
      res.writeHead(500).end(JSON.stringify({ message: 'Transient trajectory error' }))
      return
    }
    res.end(JSON.stringify(state))
    return
  }
  res.writeHead(404).end(JSON.stringify({ message: 'Unknown method' }))
})

await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
writeFileSync(logFile, 'Language server listening on random port at ' + server.address().port + ' for HTTP\\n')

const rl = createInterface({ input: process.stdin })
rl.on('line', async (line) => {
  const req = JSON.parse(line)
  appendFileSync(callsFile, JSON.stringify({ worker: process.pid, ...req }) + '\\n')
  if (req.id === undefined) return
  if (req.method === 'session/prompt') {
    const state = JSON.parse(readFileSync(dir + '/trajectory.json', 'utf8'))
    const sid = req.params.sessionId
    const upd = (update) => send({ method: 'session/update', params: { sessionId: sid, update } })
    if (state.streamLiveUpdates) {
      upd({ sessionUpdate: 'agent_thought_chunk', content: { text: 'Planning live step...' } })
      upd({ sessionUpdate: 'tool_call', toolCallId: 'live-tc-1', name: 'run_command', rawInput: { CommandLine: 'echo 1' } })
    }
    if (state.holdPrompt) {
      writeFileSync(dir + '/prompt-held', '')
      while (!existsSync(dir + '/release-prompt')) {
        if (existsSync(dir + '/trigger-tool-done') && !existsSync(dir + '/tool-done-sent')) {
          writeFileSync(dir + '/tool-done-sent', '')
          upd({ sessionUpdate: 'tool_call_update', toolCallId: 'live-tc-1', status: 'completed', rawOutput: 'ok' })
          upd({ sessionUpdate: 'agent_thought_chunk', content: { text: ' updated thought' } })
        }
        await new Promise((resolve) => setTimeout(resolve, 15))
      }
    }
    if (state.promptReply) {
      upd({ sessionUpdate: 'agent_message_chunk', content: { text: state.promptReply } })
    }
    send({ id: req.id, result: { stopReason: 'end_turn' } })
    return
  }
  send({ id: req.id, result: {} })
})
rl.on('close', () => server.close(() => process.exit(0)))
`

async function makeFixture(t, {
  steps = baseRecords,
  nativeSteps = baseRecords.map(toNative),
  summary = { status: 'CASCADE_RUN_STATUS_IDLE', not_fully_idle: 0, killed: 0, step_count: 2, last_modified_time: '2026-01-01T00:00:10Z' },
  extraState = {},
  runtimeOptions = {},
} = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-cut-cache-'))
  const config = join(home, 'config')
  const logs = join(config, 'brain', RAW, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  const transcript = join(logs, 'transcript_full.jsonl')
  writeFileSync(transcript, steps.map((s) => JSON.stringify(s)).join('\n') + '\n')
  const callsFile = join(home, 'calls.jsonl')
  writeFileSync(callsFile, '')
  const nativeFile = join(home, 'trajectory.json')
  writeFileSync(nativeFile, JSON.stringify({
    status: 'CASCADE_RUN_STATUS_IDLE',
    trajectory: { cascadeId: RAW, steps: nativeSteps },
    ...extraState,
  }))
  const fakeScript = join(home, 'fake-acp.mjs')
  writeFileSync(fakeScript, FAKE_ACP_SOURCE)

  const db = new DatabaseSync(join(config, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  const upsertSummary = (row) => {
    db.prepare('INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, ?, ?, ?, ?, ?, ?)')
      .run(
        RAW,
        row.status ?? 'CASCADE_RUN_STATUS_IDLE',
        row.not_fully_idle ?? 0,
        row.killed ?? 0,
        row.step_count,
        row.title ?? 'Cache fixture',
        JSON.stringify([`file://${home}`]),
        row.last_modified_time ?? '2026-01-01T00:00:10Z',
      )
  }
  upsertSummary(summary)

  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({
    home,
    agyDir: config,
    agyBin: process.execPath,
    agyArgs: [fakeScript, home],
    socketPath: node.path,
    log: () => {},
    ...runtimeOptions,
  })
  t.after(async () => {
    await runtime.stop()
    db.close()
    rmSync(home, { recursive: true, force: true })
  })
  runtime.start()
  await node.ready
  await until('fixture session listed', () => node.last('runtime.sessions')?.sessions.some((s) => s.session_id === SESSION))

  const calls = () => readFileSync(callsFile, 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const trajectoryCalls = () => calls().filter((c) => c.method === 'GetCascadeTrajectory').length
  const refreshList = async (title) => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: SESSION, title })
    await until('inventory updated', () => node.all('runtime.sessions').length > before)
    return node.last('runtime.sessions').sessions.find((s) => s.session_id === SESSION)
  }
  const setNative = (patch) => {
    const cur = JSON.parse(readFileSync(nativeFile, 'utf8'))
    writeFileSync(nativeFile, JSON.stringify({ ...cur, ...patch }))
  }

  return { home, transcript, db, upsertSummary, node, runtime, calls, trajectoryCalls, refreshList, setNative }
}

const textItems = (page) => page.items.filter((i) => i.kind === 'text')

test('summary_behind idle session caches non-rewind check and stops GetCascadeTrajectory storm', async (t) => {
  const { node, trajectoryCalls, refreshList } = await makeFixture(t, {
    summary: { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 2, last_modified_time: '2026-01-01T00:00:10Z' },
  })

  const first = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(textItems(first).map((i) => i.text), ['First question', 'First answer', 'Second question', 'Second answer'])
  assert.equal(trajectoryCalls(), 1, 'initial summary_behind check verifies native trajectory once')

  for (let i = 0; i < 5; i++) {
    const page = await node.peer.call('session.items', { session_id: SESSION })
    assert.equal(page.to_seq, 4)
    assert.equal(textItems(page).length, 4)
  }
  await node.peer.call('session.watch', { session_id: SESSION })
  await node.peer.call('session.item', { session_id: SESSION, item_id: textItems(first)[3].id })
  await refreshList('Refresh 1')
  await refreshList('Refresh 2')
  const row = await refreshList('Refresh 3')
  assert.equal(row.last_text, 'Second answer')
  assert.equal(trajectoryCalls(), 1, 'unchanged summary_behind state must not repeat GetCascadeTrajectory')
  await node.peer.call('session.unwatch', { session_id: SESSION })
})

test('running sessions skip GetCascadeTrajectory under both nativeWorking and hasLiveWork', async (t) => {
  // 1. nativeWorking (status = CASCADE_RUN_STATUS_RUNNING) with lagging step_count
  const { node, home, upsertSummary, trajectoryCalls, refreshList, setNative } = await makeFixture(t, {
    summary: { status: 'CASCADE_RUN_STATUS_RUNNING', step_count: 1, last_modified_time: '2026-01-01T00:00:10Z' },
    extraState: { holdPrompt: true, streamLiveUpdates: true, promptReply: 'Done live' },
  })

  for (let i = 0; i < 3; i++) {
    const page = await node.peer.call('session.items', { session_id: SESSION })
    assert.equal(textItems(page).length, 4)
  }
  await node.peer.call('session.watch', { session_id: SESSION })
  await refreshList('Running native')
  assert.equal(trajectoryCalls(), 0, 'nativeWorking session must skip GetCascadeTrajectory')

  // Also test not_fully_idle = 1 while status is IDLE
  upsertSummary({ status: 'CASCADE_RUN_STATUS_IDLE', not_fully_idle: 1, step_count: 1, last_modified_time: '2026-01-01T00:00:11Z' })
  await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(trajectoryCalls(), 0, 'not_fully_idle session must skip GetCascadeTrajectory')

  // 2. hasLiveWork (active Persona turn) even when SQLite summary says IDLE with lagging step_count
  upsertSummary({ status: 'CASCADE_RUN_STATUS_IDLE', not_fully_idle: 0, step_count: 1, last_modified_time: '2026-01-01T00:00:12Z' })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Run live turn' })
  await until('prompt held in fake ACP', () => existsSync(join(home, 'prompt-held')))
  try {
    for (let i = 0; i < 3; i++) {
      const page = await node.peer.call('session.items', { session_id: SESSION })
      assert.ok(page.items.some((item) => item.text === 'Run live turn'))
    }
    await node.peer.call('session.watch', { session_id: SESSION })
    await refreshList('Active live turn')
    assert.equal(trajectoryCalls(), 0, 'hasLiveWork session must skip GetCascadeTrajectory')
  } finally {
    writeFileSync(join(home, 'release-prompt'), '')
  }

  // Once the live turn finishes and session is idle (still lagging), one check runs and is negative-cached.
  await until('active turn settled', () => node.last('runtime.sessions')?.sessions[0]?.status !== 'working')
  setNative({ holdPrompt: false })
  await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(trajectoryCalls(), 1, 'settled lagging session checks once after turn ends')
  await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(trajectoryCalls(), 1, 'repeat read hits negative cache after turn ends')
  await node.peer.call('session.unwatch', { session_id: SESSION })
})

test('real rewind after negative-cached summary_behind invalidates cache and truncates accurately', async (t) => {
  const { node, transcript, upsertSummary, trajectoryCalls, setNative } = await makeFixture(t, {
    summary: { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 3, last_modified_time: '2026-01-01T00:00:10Z' },
  })

  // Warm negative cache in summary_behind state
  const initial = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(textItems(initial).length, 4)
  await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(trajectoryCalls(), 1)

  // Real external rewind: native trajectory shrinks to 2 steps and SQLite summary updates step_count + last_modified_time
  setNative({ trajectory: { cascadeId: RAW, steps: baseRecords.slice(0, 2).map(toNative) } })
  upsertSummary({ status: 'CASCADE_RUN_STATUS_IDLE', step_count: 2, last_modified_time: '2026-01-01T00:01:00Z' })

  const cutPage = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(textItems(cutPage).map((i) => i.text), ['First question', 'First answer'])
  assert.ok(cutPage.history.endsWith(':cut:2'), 'real rewind must apply cut immediately')
  assert.equal(trajectoryCalls(), 2, 'changed nativeCutState must invalidate negative cache')

  // Repeated reads after cut is installed must not call GetCascadeTrajectory again
  await node.peer.call('session.items', { session_id: SESSION })
  await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(trajectoryCalls(), 2)

  // Now simulate a replacement branch written on disk (branch changes), followed by another external rewind to 1 step
  // with updated last_modified_time only (same step_count = 1 after first negative-caching step_count = 1).
  const branchReplacement = { step_index: 2, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: '2026-01-02T00:00:00Z', content: 'Branched question' }
  appendFileSync(transcript, JSON.stringify(branchReplacement) + '\n')
  setNative({ trajectory: { cascadeId: RAW, steps: [...baseRecords.slice(0, 2), branchReplacement].map(toNative) } })
  upsertSummary({ status: 'CASCADE_RUN_STATUS_IDLE', step_count: 1, last_modified_time: '2026-01-02T00:00:05Z' })
  const branched = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(textItems(branched).map((i) => i.text), ['First question', 'First answer', 'Branched question'])
  const callsAfterBranch = trajectoryCalls()
  assert.ok(callsAfterBranch >= 3, 'branch change must invalidate cache')

  // Same step_count (1) but last_modified_time changes when external rewind actually cuts down to step 1
  setNative({ trajectory: { cascadeId: RAW, steps: baseRecords.slice(0, 1).map(toNative) } })
  upsertSummary({ status: 'CASCADE_RUN_STATUS_IDLE', step_count: 1, last_modified_time: '2026-01-02T00:00:20Z' })
  const cutAgain = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(textItems(cutAgain).map((i) => i.text), ['First question'])
  assert.ok(cutAgain.history.endsWith(':cut:1'), 'last_modified_time change must invalidate negative cache and apply cut')
  assert.equal(trajectoryCalls(), callsAfterBranch + 1)
})

test('transient GetCascadeTrajectory errors back off and still allow immediate recovery when native state changes', async (t) => {
  const { node, upsertSummary, trajectoryCalls, setNative } = await makeFixture(t, {
    summary: { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 2, last_modified_time: '2026-01-01T00:00:10Z' },
    extraState: { failTrajectory: true },
    runtimeOptions: { cutRetryMs: 10_000 },
  })

  for (let i = 0; i < 4; i++) {
    const page = await node.peer.call('session.items', { session_id: SESSION })
    assert.equal(textItems(page).length, 4)
  }
  assert.equal(trajectoryCalls(), 1, 'failed GetCascadeTrajectory must back off instead of retrying every request')

  // Recover native endpoint and advance summary to a genuine rewind
  setNative({ failTrajectory: false, trajectory: { cascadeId: RAW, steps: baseRecords.slice(0, 2).map(toNative) } })
  upsertSummary({ status: 'CASCADE_RUN_STATUS_IDLE', step_count: 2, last_modified_time: '2026-01-01T00:02:00Z' })
  const rewound = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(textItems(rewound).map((i) => i.text), ['First question', 'First answer'])
  assert.ok(rewound.history.endsWith(':cut:2'))
  assert.equal(trajectoryCalls(), 2)
})

test('stepsToItems summaryOnly matches full mapping at every prefix and reuses summaryState without touching prefix steps', (t) => {
  const brain = mkdtempSync(join(tmpdir(), 'agy-summary-parity-'))
  const logs = join(brain, '.system_generated', 'logs')
  mkdirSync(logs, { recursive: true })
  t.after(() => rmSync(brain, { recursive: true, force: true }))

  const rawSteps = [
    { step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: '2026-01-01T00:00:00Z', content: 'Start' },
    { step_index: 1, type: 'PLANNER_RESPONSE', source: 'MODEL', status: 'DONE', created_at: '2026-01-01T00:00:01Z', thinking: 'hmm', content: 'Working', tool_calls: [{ name: 'run_command', args: { CommandLine: 'ls' } }], model: 'gemini-4-carbon-high', input_tokens: 1200, cache_read_tokens: 300, output_tokens: 40, context_window: 1000000 },
    { step_index: 2, type: 'GENERIC', source: 'MODEL', status: 'DONE', created_at: '2026-01-01T00:00:02Z', content: 'Created At: 2026-01-01T00:00:02Z\nOutput:\nfile.txt' },
    { step_index: 3, type: 'PLANNER_RESPONSE', source: 'MODEL', status: 'DONE', created_at: '2026-01-01T00:00:03Z', content: 'Completed reply', model: 'gemini-4-carbon-high', input_tokens: 1500, output_tokens: 60 },
    { step_index: 4, type: 'SYSTEM_MESSAGE', source: 'SYSTEM', status: 'DONE', created_at: '2026-01-01T00:00:04Z', content: 'Background job finished' },
    { step_index: 5, type: 'PLANNER_RESPONSE', source: 'MODEL', status: 'DONE', created_at: '2026-01-01T00:00:05Z', content: 'Wake follow-up', model: 'claude-opus-5-5-high', input_tokens: 2000, cache_write_tokens: 500, output_tokens: 90 },
    { step_index: 6, type: 'CHECKPOINT', source: 'SYSTEM', status: 'DONE', created_at: '2026-01-01T00:00:06Z' },
    { step_index: 7, type: 'PERSONA_MODE_NOTICE', status: 'DONE', created_at: '2026-01-01T00:00:07Z', content: 'Mode notice' },
    { step_index: 8, type: 'ERROR_MESSAGE', source: 'MODEL', status: 'ERROR', created_at: '2026-01-01T00:00:08Z', error: 'Rate limit exceeded', turn_error: { turn_error_kind: 'usage_limit' } },
    { step_index: 9, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: '2026-01-01T00:00:09Z', content: 'Retry' },
    { step_index: 10, type: 'ERROR_MESSAGE', source: 'MODEL', status: 'ERROR', created_at: '2026-01-01T00:00:10Z', error: 'Before work error' },
    { step_index: 11, type: 'USER_INPUT', source: 'SYSTEM', status: 'DONE', created_at: '2026-01-01T00:00:11Z', content: 'Non-explicit ignored' },
    { step_index: 12, type: 'PERSONA_AUTONOMOUS_WAKE', status: 'DONE', created_at: '2026-01-01T00:00:12Z' },
    { step_index: 13, type: 'PLANNER_RESPONSE', source: 'MODEL', status: 'DONE', created_at: '2026-01-01T00:00:13Z', content: 'Final answer', input_tokens: 800, output_tokens: 25 },
  ]
  writeFileSync(join(logs, 'transcript_full.jsonl'), rawSteps.map((s) => JSON.stringify(s)).join('\n') + '\n')
  rememberObservedWake(brain, readTranscript(brain, true).steps, 5)
  const steps = readTranscript(brain, true).steps

  const pick = ({ toSeq, lastText, turnError, usage }) => ({ toSeq, lastText, turnError, usage })
  for (let k = 0; k <= steps.length; k++) {
    const slice = steps.slice(0, k)
    const full = stepsToItems(slice, { full: false, skipCache: true })
    const summaryOnly = stepsToItems(slice, { full: false, summaryOnly: true })
    assert.deepEqual(pick(summaryOnly), pick(full), `summaryOnly parity at prefix length ${k}`)

    // Resuming from prefix [0..k) over full `steps` must match full computation without reading steps[0..k-1]
    let prefixReads = 0
    const guarded = steps.map((step, index) => index < k
      ? new Proxy(step, { get(target, prop, receiver) { prefixReads++; return Reflect.get(target, prop, receiver) } })
      : step)
    const resumed = stepsToItems(guarded, {
      full: false,
      summaryOnly: true,
      startIndex: k,
      summaryState: summaryOnly.summaryState,
    })
    assert.equal(prefixReads, 0, `resuming at startIndex=${k} must not touch prefix steps`)
    assert.deepEqual(pick(resumed), pick(stepsToItems(steps, { full: false, skipCache: true })), `resumed parity from k=${k}`)
  }
})

test('summarizeTranscript reuses prefix cache across active synthetic turns, pending turns, and appends on a 9MB transcript', async (t) => {
  const payload = 'x'.repeat(2800)
  const bigSteps = []
  for (let i = 0; i < 3200; i++) {
    const type = i % 10 === 0 ? 'USER_INPUT' : i % 2 === 1 ? 'PLANNER_RESPONSE' : 'GENERIC'
    bigSteps.push(type === 'USER_INPUT'
      ? { step_index: i, source: 'USER_EXPLICIT', type, status: 'DONE', created_at: AT, content: `Prompt ${i} ${payload.slice(0, 300)}` }
      : type === 'PLANNER_RESPONSE'
        ? { step_index: i, source: 'MODEL', type, status: 'DONE', created_at: AT, thinking: payload, content: `Reply ${i} ${payload.slice(0, 400)}`, model: 'gemini-4-carbon-high', input_tokens: 10000 + i, output_tokens: 10, context_window: 1000000 }
        : { step_index: i, source: 'MODEL', type, status: 'DONE', created_at: AT, content: `Created At: ${AT}\nOutput:\n${payload}` })
  }
  const rawBytes = bigSteps.reduce((sum, s) => sum + JSON.stringify(s).length + 1, 0)
  assert.ok(rawBytes >= 8.5 * 1024 * 1024, `expected ~9MB fixture, got ${rawBytes}`)

  // Direct prefix-reuse timing proof on the 9MB step array: 50 incremental live-step summaries stay sub-millisecond each.
  const baseSummary = stepsToItems(bigSteps, { full: false, summaryOnly: true })
  const liveTail = [
    { step_index: 3200, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', created_at: AT, content: 'Next' },
    { step_index: 3201, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'RUNNING', created_at: AT, thinking: 'step 0' },
  ]
  const combined = [...bigSteps, ...liveTail]
  const tSummary0 = performance.now()
  for (let k = 0; k < 50; k++) {
    liveTail[1].thinking = `step ${k}`
    const inc = stepsToItems(combined, { full: false, summaryOnly: true, startIndex: bigSteps.length, summaryState: baseSummary.summaryState })
    assert.equal(inc.toSeq, 3202)
    assert.equal(inc.usage.outputTokens, 16000)
  }
  const incSummaryMs = performance.now() - tSummary0
  assert.ok(incSummaryMs < 250, `50 incremental summaries on 9MB prefix took ${incSummaryMs.toFixed(1)}ms`)

  const { node, home, transcript, trajectoryCalls, refreshList } = await makeFixture(t, {
    steps: bigSteps,
    nativeSteps: [],
    // Lagging summary_behind even before the turn starts
    summary: { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 3190, last_modified_time: '2026-01-01T00:00:10Z' },
    extraState: { holdPrompt: true, streamLiveUpdates: true, promptReply: 'Active turn final reply' },
  })

  const initialRow = node.last('runtime.sessions').sessions.find((s) => s.session_id === SESSION)
  assert.equal(initialRow.rev, 3200)
  assert.equal(initialRow.model, 'gemini-4-carbon-high')
  assert.equal(initialRow.context_window, 1000000)
  assert.equal(initialRow.output_tokens, 16000)

  // Start an active synthetic turn with live thought + tool_call steps
  await node.peer.call('session.send', { session_id: SESSION, text: 'Run active synthetic turn' })
  await until('active turn prompt held', () => existsSync(join(home, 'prompt-held')))
  await until('live tool step reflected in listSessions', () => {
    const row = node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)
    return row?.status === 'working' && row?.rev >= 3203
  })

  try {
    // Trigger 6 listSessions refreshes during the active synthetic turn while live steps mutate in place.
    // Note: runtime-sdk debounces session.rename -> sessionsChanged() by 300ms per refresh (~1800ms debounce floor).
    const t0 = performance.now()
    for (let i = 0; i < 6; i++) {
      if (i === 3) {
        writeFileSync(join(home, 'trigger-tool-done'), '')
        await until('tool update delivered', () => existsSync(join(home, 'tool-done-sent')))
      }
      const row = await refreshList(`Live refresh ${i}`)
      assert.equal(row.status, 'working')
      assert.equal(row.model, 'gemini-4-carbon-high')
      assert.equal(row.output_tokens, 16000)
      assert.ok(row.rev >= 3203, `expected synthetic rev >= 3203, got ${row.rev}`)
    }
    const elapsedMs = performance.now() - t0
    const tItems0 = performance.now()
    for (let i = 0; i < 3; i++) {
      const page = await node.peer.call('session.items', { session_id: SESSION, limit: 20 })
      assert.ok(page.to_seq >= 3203)
    }
    const itemsMs = performance.now() - tItems0
    assert.ok(itemsMs < 1500, `3 active-turn session.items calls on 9MB transcript took ${itemsMs.toFixed(1)}ms`)
    assert.equal(trajectoryCalls(), 0, 'active turn must never trigger GetCascadeTrajectory')
    const afterTool = await refreshList('After live tool')
    assert.ok(afterTool.rev >= 3204, `expected rev >= 3204 after tool_call_update, got ${afterTool.rev}`)
    assert.ok(elapsedMs < 6000, `6 active-turn listSessions refreshes on 9MB transcript took ${elapsedMs.toFixed(1)}ms`)
  } finally {
    writeFileSync(join(home, 'release-prompt'), '')
  }

  // Turn completes -> enters synthetic unexportedTurns before disk export arrives.
  await until('pending unexported reply visible in inventory', () => {
    const row = node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)
    return row?.status !== 'working' && row?.last_text === 'Active turn final reply'
  })
  const pendingRow = await refreshList('Pending unexported')
  assert.equal(pendingRow.last_text, 'Active turn final reply')
  assert.equal(pendingRow.output_tokens, 16000)

  // Append exported steps to disk -> incremental append prefix reuse updates listSessions cleanly.
  appendFileSync(transcript, [
    { step_index: 3200, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', created_at: '2026-01-01T00:01:00Z', content: 'Run active synthetic turn' },
    { step_index: 3201, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-01-01T00:01:01Z', content: 'Active turn final reply', model: 'gemini-4-carbon-high', input_tokens: 15000, output_tokens: 45 },
  ].map((s) => JSON.stringify(s)).join('\n') + '\n')
  const exportedRow = await refreshList('Exported reply')
  assert.equal(exportedRow.rev, 3202)
  assert.equal(exportedRow.last_text, 'Active turn final reply')
  assert.equal(exportedRow.context_tokens, 15000)
  assert.equal(exportedRow.output_tokens, 16045)
})

test('authoritative GetCascadeTrajectory idle verification resolves summary_behind to done/idle and invalidates on any transcript or summary change', async (t) => {
  const { node, transcript, upsertSummary, trajectoryCalls, refreshList, setNative } = await makeFixture(t, {
    summary: { status: 'CASCADE_RUN_STATUS_IDLE', step_count: 2, last_modified_time: '2026-01-01T00:00:10Z' },
  })
  const row = () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)

  // Before session.items, listSessions must stay fail-closed unknown and never call GetCascadeTrajectory.
  assert.equal(row().status, 'unknown')
  assert.equal(trajectoryCalls(), 0)

  // Watching from seq 4 has no turn_duration yet; once session.items verifies the idle native trajectory,
  // cutChecks is cleared before syncTranscriptAppend so the live watch stream receives turn_duration immediately.
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: 4 })
  const watchedBefore = node.all('session.items').length

  // Reading session.items verifies the native trajectory once; matching idle trajectory resolves status to done and emits turn_duration.
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(trajectoryCalls(), 1)
  assert.equal(page.items.at(-1).kind, 'turn_duration')
  assert.equal(page.items.at(-1).seq, 4)
  await until('watched stream receives live turn_duration on verified summary_behind', () =>
    node.all('session.items').slice(watchedBefore).some((batch) =>
      batch.session_id === SESSION && batch.items?.some((it) => it.kind === 'turn_duration' && it.seq === 4)),
  )
  await node.peer.call('session.unwatch', { session_id: SESSION })
  await until('inventory updates to done after native trajectory verification', () => row()?.status === 'done')
  assert.equal((await refreshList('Verified done cached')).status, 'done')
  assert.equal(trajectoryCalls(), 1, 'subsequent refreshes reuse cached verification without extra RPCs')

  // Changing only last_modified_time (same step_count) invalidates both verifiedNoCutSteps and listedTranscriptCache.
  upsertSummary({ status: 'CASCADE_RUN_STATUS_IDLE', step_count: 2, last_modified_time: '2026-01-01T00:00:15Z' })
  assert.equal((await refreshList('Timestamp changed only')).status, 'unknown')

  // Appending a new unverified step invalidates verifiedNoCutSteps immediately and drops back to unknown.
  const nextUser = { step_index: 4, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: '2026-01-01T00:00:20Z', content: 'Third question' }
  appendFileSync(transcript, JSON.stringify(nextUser) + '\n')
  assert.equal((await refreshList('Unverified append')).status, 'unknown')

  // When GetCascadeTrajectory reports RUNNING (not IDLE), summary_behind stays unknown without turn_duration.
  setNative({ status: 'CASCADE_RUN_STATUS_RUNNING', trajectory: { cascadeId: RAW, steps: [...baseRecords, nextUser].map(toNative) } })
  const runningPage = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(trajectoryCalls(), 2)
  assert.notEqual(runningPage.items.at(-1).kind, 'turn_duration')
  assert.equal((await refreshList('Still running natively')).status, 'unknown')

  // Once native trajectory is IDLE at the USER_INPUT tail (not a completed reply), verified status resolves to idle (no turn_duration).
  setNative({ status: 'CASCADE_RUN_STATUS_IDLE' })
  upsertSummary({ status: 'CASCADE_RUN_STATUS_IDLE', step_count: 2, last_modified_time: '2026-01-01T00:00:25Z' })
  const idlePage = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(trajectoryCalls(), 3)
  assert.notEqual(idlePage.items.at(-1).kind, 'turn_duration')
  await until('inventory updates to idle for verified non-reply tail', () => row()?.status === 'idle')
})
